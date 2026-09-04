package agentteamsbridge

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/haochase/haowork/internal/model"
)

type MatrixOutbound struct {
	MissionID       string         `json:"missionID"`
	RunID           string         `json:"runID"`
	WorkItemID      string         `json:"workItemID"`
	WorkspaceDigest string         `json:"workspaceDigest"`
	ArtifactRef     string         `json:"artifactRef"`
	Artifact        MatrixArtifact `json:"artifact,omitempty"`
}

// SelectRoom binds subsequent Sync calls to the Leader room observed from the
// official Team status. The room is never accepted from a Matrix event body.
func (client *MatrixV3Client) SelectRoom(roomID string) error {
	roomID = strings.TrimSpace(roomID)
	if client == nil || roomID == "" || !strings.HasPrefix(roomID, "!") {
		return errors.New("Matrix v3 leader room ID is required")
	}
	client.defaultRoomID = roomID
	return nil
}

type MatrixEvent struct {
	ID              string              `json:"id"`
	RoomID          string              `json:"roomID"`
	Kind            string              `json:"kind"`
	Summary         string              `json:"summary"`
	SummarySHA256   string              `json:"summarySHA256,omitempty"`
	WorkspaceDigest string              `json:"workspaceDigest"`
	MissionID       string              `json:"missionID,omitempty"`
	RunID           string              `json:"runID,omitempty"`
	WorkItemID      string              `json:"workItemID,omitempty"`
	CorrelationID   string              `json:"correlationID,omitempty"`
	SenderID        string              `json:"senderID"`
	SenderRole      string              `json:"senderRole"`
	AgentFunction   model.AgentFunction `json:"agentFunction"`
	Artifacts       []MatrixArtifact    `json:"artifacts,omitempty"`
}

// MatrixMember is the minimal membership record required to attribute a
// Matrix sender. It deliberately excludes profile fields and message content.
type MatrixMember struct {
	UserID     string `json:"userID"`
	Membership string `json:"membership"`
}

// MatrixV3Config configures the official Matrix Client-Server v3 adapter.
// AccessToken is preferred when the caller already owns a mounted credential;
// Username and Password are only used by Login.
type MatrixV3Config struct {
	BaseURL                   string
	Username                  string
	Password                  string
	AccessToken               string
	AppServiceToken           string
	ExpectedUserID            string
	DefaultRoomID             string
	AllowInsecureClusterLocal bool
	Client                    *http.Client
	MaxBodyBytes              int64
}

// MatrixV3Client talks only to the official Matrix Client-Server v3 API. It
// does not retain Matrix message bodies after deriving their SHA-256 summary.
type MatrixV3Client struct {
	base            *url.URL
	client          *http.Client
	username        string
	password        string
	accessToken     string
	appServiceToken string
	expectedUserID  string
	defaultRoomID   string
	maxBodyBytes    int64
}

const defaultMatrixV3MaxBodyBytes int64 = 1 << 20
const matrixV3SyncTimeout = time.Second

func NewMatrixV3Client(config MatrixV3Config) (*MatrixV3Client, error) {
	base, err := url.Parse(strings.TrimRight(strings.TrimSpace(config.BaseURL), "/"))
	clusterLocalHTTP := base != nil && base.Scheme == "http" && config.AllowInsecureClusterLocal && isClusterLocalHost(base.Hostname())
	if err != nil || base.Scheme == "" || base.Host == "" || base.User != nil || (base.Scheme != "https" && !isLoopbackHost(base.Host) && !clusterLocalHTTP) {
		return nil, ErrInsecureControlPlane
	}
	client := config.Client
	if client == nil {
		client = http.DefaultClient
	}
	copy := *client
	copy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	maxBodyBytes := config.MaxBodyBytes
	if maxBodyBytes <= 0 {
		maxBodyBytes = defaultMatrixV3MaxBodyBytes
	}
	return &MatrixV3Client{
		base: base, client: &copy, username: strings.TrimSpace(config.Username), password: config.Password,
		accessToken: strings.TrimSpace(config.AccessToken), appServiceToken: strings.TrimSpace(config.AppServiceToken), expectedUserID: strings.TrimSpace(config.ExpectedUserID), defaultRoomID: strings.TrimSpace(config.DefaultRoomID), maxBodyBytes: maxBodyBytes,
	}, nil
}

func isClusterLocalHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	if !strings.HasSuffix(host, ".svc.cluster.local") {
		return false
	}
	labels := strings.Split(host, ".")
	if len(labels) < 5 {
		return false
	}
	for _, label := range labels {
		if label == "" || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, character := range label {
			if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '-' {
				return false
			}
		}
	}
	return true
}

// Login obtains a user-scoped Matrix access token using either the official
// AppService flow or the password flow used by standalone deployments.
func (client *MatrixV3Client) Login(ctx context.Context) error {
	if client == nil || client.base == nil || client.username == "" {
		return errors.New("Matrix v3 login credentials are required")
	}
	loginToken := ""
	loginBody := map[string]any{
		"type":       "m.login.password",
		"identifier": map[string]string{"type": "m.id.user", "user": client.username},
		"password":   client.password,
	}
	if client.appServiceToken != "" {
		loginToken = client.appServiceToken
		client.appServiceToken = ""
		loginBody = map[string]any{
			"type":       "m.login.application_service",
			"identifier": map[string]string{"type": "m.id.user", "user": client.username},
		}
	} else if client.password == "" {
		return errors.New("Matrix v3 login credentials are required")
	}
	var response struct {
		AccessToken string `json:"access_token"`
		UserID      string `json:"user_id"`
	}
	err := client.doJSON(ctx, http.MethodPost, "/_matrix/client/v3/login", loginToken, loginBody, &response)
	if err != nil {
		return err
	}
	if strings.TrimSpace(response.AccessToken) == "" || strings.TrimSpace(response.UserID) == "" {
		return errors.New("Matrix v3 login returned an incomplete identity")
	}
	if client.expectedUserID != "" && strings.TrimSpace(response.UserID) != client.expectedUserID {
		return errors.New("Matrix v3 login returned an unexpected identity")
	}
	client.accessToken = response.AccessToken
	return nil
}

func (client *MatrixV3Client) JoinedRooms(ctx context.Context) ([]string, error) {
	var response struct {
		JoinedRooms []string `json:"joined_rooms"`
	}
	if err := client.doJSON(ctx, http.MethodGet, "/_matrix/client/v3/joined_rooms", client.token(), nil, &response); err != nil {
		return nil, err
	}
	rooms := make([]string, 0, len(response.JoinedRooms))
	for _, roomID := range response.JoinedRooms {
		roomID = strings.TrimSpace(roomID)
		if roomID == "" {
			return nil, errors.New("Matrix joined_rooms contains an empty room ID")
		}
		rooms = append(rooms, roomID)
	}
	return rooms, nil
}

// Checkpoint captures an opaque sync cursor before a new delegation is sent,
// preventing older room history from being attributed to the new run.
func (client *MatrixV3Client) Checkpoint(ctx context.Context) (string, error) {
	var response matrixSyncResponse
	if err := client.doJSON(ctx, http.MethodGet, "/_matrix/client/v3/sync?timeout=0", client.token(), nil, &response); err != nil {
		return "", err
	}
	cursor := strings.TrimSpace(response.NextBatch)
	if cursor == "" {
		return "", errors.New("Matrix v3 checkpoint returned no next_batch")
	}
	return cursor, nil
}

// Send puts a compact, governed reference into Matrix. The transaction ID is
// stable for the same Mission/Run/WorkItem/Artifact tuple, and this method
// intentionally makes exactly one write request.
func (client *MatrixV3Client) Send(ctx context.Context, roomID string, outbound MatrixOutbound) error {
	roomID = strings.TrimSpace(roomID)
	workspaceDigest := strings.ToLower(strings.TrimSpace(outbound.WorkspaceDigest))
	if roomID == "" || strings.TrimSpace(outbound.MissionID) == "" || strings.TrimSpace(outbound.RunID) == "" || strings.TrimSpace(outbound.WorkItemID) == "" || !isLowerSHA256(workspaceDigest) {
		return errors.New("Matrix v3 send requires room, mission, run, and work item IDs")
	}
	txnID := MatrixTransactionID(outbound)
	path := "/_matrix/client/v3/rooms/" + encodeMatrixRoomID(roomID) + "/send/m.room.message/" + url.PathEscape(txnID)
	var response struct {
		EventID string `json:"event_id"`
	}
	body := map[string]any{
		"msgtype":                      "m.text",
		"body":                         "HAOWORK_CORRELATION_ID: " + txnID + "\n\nHaowork governed mission assigned. Read the attached mission reference and reply with a concise completion summary. Repeat the correlation line exactly as the first line of your reply.",
		"org.haowork.mission_id":       strings.TrimSpace(outbound.MissionID),
		"org.haowork.run_id":           strings.TrimSpace(outbound.RunID),
		"org.haowork.work_item_id":     strings.TrimSpace(outbound.WorkItemID),
		"org.haowork.correlation_id":   txnID,
		"org.haowork.workspace_digest": workspaceDigest,
		"org.haowork.artifact_ref":     strings.TrimSpace(outbound.ArtifactRef),
	}
	artifact, hasArtifact, err := normalizedMatrixArtifact(outbound.ArtifactRef, outbound.Artifact)
	if err != nil {
		return err
	}
	if hasArtifact {
		body["org.haowork.artifacts"] = []MatrixArtifact{artifact}
	}
	if err := client.doJSON(ctx, http.MethodPut, path, client.token(), body, &response); err != nil {
		return err
	}
	if strings.TrimSpace(response.EventID) == "" {
		return errors.New("Matrix v3 send returned no event_id")
	}
	return nil
}

func normalizedMatrixArtifact(reference string, artifact MatrixArtifact) (MatrixArtifact, bool, error) {
	reference = strings.TrimSpace(reference)
	artifact.URI = strings.TrimSpace(artifact.URI)
	artifact.SHA256 = strings.ToLower(strings.TrimSpace(artifact.SHA256))
	artifact.EnvironmentID = strings.TrimSpace(artifact.EnvironmentID)
	if reference == "" && artifact.URI == "" && artifact.SHA256 == "" && artifact.EnvironmentID == "" && artifact.Size == 0 {
		return MatrixArtifact{}, false, nil
	}
	if reference == "" || artifact.URI != reference || !isLowerSHA256(artifact.SHA256) || artifact.EnvironmentID == "" || artifact.Size < 0 {
		return MatrixArtifact{}, false, errors.New("Matrix artifact metadata must match the governed artifact reference")
	}
	return artifact, true, nil
}

// MatrixTransactionID returns the bounded, deterministic Matrix transaction
// key used for write idempotency without persisting a raw payload.
func MatrixTransactionID(outbound MatrixOutbound) string {
	value := strings.Join([]string{strings.TrimSpace(outbound.MissionID), strings.TrimSpace(outbound.RunID), strings.TrimSpace(outbound.WorkItemID), strings.TrimSpace(outbound.ArtifactRef)}, "\x00")
	digest := sha256.Sum256([]byte(value))
	return "haowork-" + hex.EncodeToString(digest[:16])
}

// Messages reads a room timeline using Matrix's opaque `from` token. The
// returned MatrixPage retains only structural metadata and body hashes.
func (client *MatrixV3Client) Messages(ctx context.Context, roomID, cursor string) (MatrixPage, error) {
	roomID = strings.TrimSpace(roomID)
	if roomID == "" {
		return MatrixPage{}, errors.New("Matrix v3 messages requires a room ID")
	}
	query := url.Values{"dir": []string{"f"}}
	if cursor != "" {
		query.Set("from", cursor)
	}
	path := "/_matrix/client/v3/rooms/" + encodeMatrixRoomID(roomID) + "/messages?" + query.Encode()
	var response matrixMessagesResponse
	if err := client.doJSON(ctx, http.MethodGet, path, client.token(), nil, &response); err != nil {
		return MatrixPage{}, err
	}
	return matrixTimelinePage(roomID, response.End, strings.TrimSpace(response.End) != "", response.Chunk)
}

func matrixTimelinePage(roomID, nextCursor string, more bool, events []matrixTimelineEvent) (MatrixPage, error) {
	page := MatrixPage{NextCursor: nextCursor, More: more}
	for _, event := range events {
		if event.Type != "m.room.message" {
			continue
		}
		if strings.TrimSpace(event.EventID) == "" || strings.TrimSpace(event.Sender) == "" {
			return MatrixPage{}, errors.New("Matrix message event must include event_id and sender")
		}
		if event.RoomID != "" && event.RoomID != roomID {
			return MatrixPage{}, errors.New("Matrix message event belongs to a different room")
		}
		bodyDigest := sha256.Sum256([]byte(event.Content.Body))
		kind, ok := matrixMessageKind(event.Content.MsgType)
		if !ok {
			continue
		}
		page.Events = append(page.Events, MatrixEvent{
			ID: event.EventID, RoomID: roomID, Kind: kind, SummarySHA256: hex.EncodeToString(bodyDigest[:]),
			WorkspaceDigest: strings.TrimSpace(event.Content.WorkspaceDigest), MissionID: strings.TrimSpace(event.Content.MissionID), RunID: strings.TrimSpace(event.Content.RunID), WorkItemID: strings.TrimSpace(event.Content.WorkItemID), CorrelationID: matrixCorrelationID(event.Content.Body), SenderID: event.Sender,
			Artifacts: append([]MatrixArtifact(nil), event.Content.Artifacts...),
		})
	}
	return page, nil
}

func matrixCorrelationID(body string) string {
	found := ""
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !strings.HasPrefix(line, "HAOWORK_CORRELATION_ID:") {
			continue
		}
		value := strings.TrimSpace(strings.TrimPrefix(line, "HAOWORK_CORRELATION_ID:"))
		if line == "HAOWORK_CORRELATION_ID: "+value && isMatrixCorrelationID(value) {
			if found != "" && found != value {
				return ""
			}
			found = value
		}
	}
	return found
}

func isMatrixCorrelationID(value string) bool {
	if len(value) != len("haowork-")+32 || !strings.HasPrefix(value, "haowork-") {
		return false
	}
	for _, character := range value[len("haowork-"):] {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func matrixMessageKind(msgType string) (string, bool) {
	switch strings.TrimSpace(msgType) {
	case "m.notice":
		return "notice", true
	case "m.text":
		return "stdout", true
	default:
		return "", false
	}
}

// Members reads official Matrix membership events for one room. It keeps only
// membership and state_key so later attribution can compare observed senders.
func (client *MatrixV3Client) Members(ctx context.Context, roomID string) ([]MatrixMember, error) {
	roomID = strings.TrimSpace(roomID)
	if roomID == "" {
		return nil, errors.New("Matrix v3 members requires a room ID")
	}
	var response matrixMembersResponse
	if err := client.doJSON(ctx, http.MethodGet, "/_matrix/client/v3/rooms/"+encodeMatrixRoomID(roomID)+"/members", client.token(), nil, &response); err != nil {
		return nil, err
	}
	members := make([]MatrixMember, 0, len(response.Chunk))
	for _, event := range response.Chunk {
		if event.Type != "m.room.member" || strings.TrimSpace(event.StateKey) == "" || strings.TrimSpace(event.Content.Membership) == "" {
			continue
		}
		members = append(members, MatrixMember{UserID: event.StateKey, Membership: event.Content.Membership})
	}
	return members, nil
}

// Sync implements the existing bridge interface while using the configured
// official room. Task 5 selects the exact room from observed Team status.
func (client *MatrixV3Client) Sync(ctx context.Context, cursor string) (MatrixPage, error) {
	if client == nil || client.defaultRoomID == "" {
		return MatrixPage{}, errors.New("Matrix v3 default room ID is required for Sync")
	}
	query := url.Values{"timeout": []string{strconv.FormatInt(int64(matrixV3SyncTimeout/time.Millisecond), 10)}}
	if strings.TrimSpace(cursor) != "" {
		query.Set("since", cursor)
	}
	var response matrixSyncResponse
	if err := client.doJSON(ctx, http.MethodGet, "/_matrix/client/v3/sync?"+query.Encode(), client.token(), nil, &response); err != nil {
		return MatrixPage{}, err
	}
	if strings.TrimSpace(response.NextBatch) == "" {
		return MatrixPage{}, errors.New("Matrix v3 sync returned no next_batch")
	}
	room, exists := response.Rooms.Join[client.defaultRoomID]
	if !exists {
		return MatrixPage{NextCursor: response.NextBatch}, nil
	}
	return matrixTimelinePage(client.defaultRoomID, response.NextBatch, false, room.Timeline.Events)
}

type matrixMessageContent struct {
	MsgType         string           `json:"msgtype"`
	Body            string           `json:"body"`
	WorkspaceDigest string           `json:"org.haowork.workspace_digest"`
	MissionID       string           `json:"org.haowork.mission_id"`
	RunID           string           `json:"org.haowork.run_id"`
	WorkItemID      string           `json:"org.haowork.work_item_id"`
	Artifacts       []MatrixArtifact `json:"org.haowork.artifacts"`
}

type matrixTimelineEvent struct {
	Type    string `json:"type"`
	EventID string `json:"event_id"`
	Sender  string `json:"sender"`
	RoomID  string `json:"room_id,omitempty"`
	// These official Matrix event containers are decoded for type safety,
	// but never persisted or used as a governance identity.
	OriginServerTS int64                      `json:"origin_server_ts,omitempty"`
	Unsigned       map[string]json.RawMessage `json:"unsigned,omitempty"`
	Content        matrixMessageContent       `json:"content"`
}

type matrixMessagesResponse struct {
	Start string                `json:"start"`
	End   string                `json:"end"`
	Chunk []matrixTimelineEvent `json:"chunk"`
}

type matrixSyncResponse struct {
	NextBatch string `json:"next_batch"`
	Rooms     struct {
		Join map[string]struct {
			Timeline struct {
				Events []matrixTimelineEvent `json:"events"`
			} `json:"timeline"`
		} `json:"join"`
	} `json:"rooms"`
}

type matrixMembersResponse struct {
	Chunk []struct {
		Type     string `json:"type"`
		StateKey string `json:"state_key"`
		Content  struct {
			Membership string `json:"membership"`
		} `json:"content"`
	} `json:"chunk"`
}

func (client *MatrixV3Client) token() string {
	if client == nil {
		return ""
	}
	return strings.TrimSpace(client.accessToken)
}

func (client *MatrixV3Client) doJSON(ctx context.Context, method, path, token string, input, output any) error {
	if client == nil || client.base == nil || client.client == nil {
		return errors.New("Matrix v3 client is required")
	}
	relative, err := url.Parse(path)
	if err != nil || relative.IsAbs() || !strings.HasPrefix(relative.Path, "/_matrix/client/v3/") {
		return errors.New("invalid Matrix v3 endpoint")
	}
	if relative.Path != "/_matrix/client/v3/login" && strings.TrimSpace(token) == "" {
		return errors.New("Matrix v3 access token is required")
	}
	endpoint := client.base.ResolveReference(relative)
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint.String(), body)
	if err != nil {
		return err
	}
	if strings.TrimSpace(token) != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("Matrix v3 %s %s returned %s", method, relative.Path, response.Status)
	}
	if output == nil || response.StatusCode == http.StatusNoContent {
		return nil
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, client.maxBodyBytes+1))
	if err := decoder.Decode(output); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("Matrix v3 response contains trailing JSON")
		}
		return err
	}
	return nil
}

func encodeMatrixRoomID(roomID string) string {
	return strings.ReplaceAll(url.PathEscape(roomID), "!", "%21")
}

type MatrixArtifact struct {
	Kind          string `json:"kind"`
	URI           string `json:"uri"`
	SHA256        string `json:"sha256"`
	EnvironmentID string `json:"environmentID"`
	Size          int64  `json:"size"`
}

type MatrixPage struct {
	NextCursor string        `json:"nextCursor"`
	More       bool          `json:"more"`
	Events     []MatrixEvent `json:"events"`
}

type MatrixClient interface {
	Send(context.Context, string, MatrixOutbound) error
	Sync(context.Context, string) (MatrixPage, error)
}

type HTTPMatrixClient struct {
	base     *url.URL
	client   *http.Client
	identity string
}

func NewHTTPMatrixClient(baseURL string, client *http.Client, identity string) *HTTPMatrixClient {
	base, _ := url.Parse(strings.TrimRight(baseURL, "/"))
	if client == nil {
		client = http.DefaultClient
	}
	copy := *client
	copy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &HTTPMatrixClient{base: base, client: &copy, identity: strings.TrimSpace(identity)}
}

func (client *HTTPMatrixClient) Send(ctx context.Context, roomID string, outbound MatrixOutbound) error {
	return client.do(ctx, http.MethodPost, "rooms/"+url.PathEscape(roomID)+"/send", outbound, nil)
}
func (client *HTTPMatrixClient) Sync(ctx context.Context, cursor string) (MatrixPage, error) {
	var page MatrixPage
	err := client.do(ctx, http.MethodGet, "sync?cursor="+url.QueryEscape(cursor), nil, &page)
	return page, err
}
func (client *HTTPMatrixClient) Stop(ctx context.Context, roomID string) error {
	return client.do(ctx, http.MethodPost, "rooms/"+url.PathEscape(roomID)+"/stop", map[string]bool{"stop": true}, nil)
}

func (client *HTTPMatrixClient) do(ctx context.Context, method, relative string, input, output any) error {
	if client == nil || client.base == nil || (client.base.Scheme != "https" && !isLoopbackHost(client.base.Host)) {
		return ErrInsecureControlPlane
	}
	endpoint := strings.TrimRight(client.base.String(), "/") + "/api/v1/matrix/" + relative
	var body []byte
	var err error
	if input != nil {
		body, err = json.Marshal(input)
		if err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	response, err := client.client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if client.identity != "" && response.Header.Get("X-AgentTeams-Identity") != client.identity {
		return ErrIdentityMismatch
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("Matrix %s %s returned %s", method, relative, response.Status)
	}
	if output == nil || response.StatusCode == http.StatusNoContent {
		return nil
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("Matrix response contains trailing JSON")
	}
	return nil
}
