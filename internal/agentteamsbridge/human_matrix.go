package agentteamsbridge

import (
	"context"
	"errors"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
)

// HumanMatrixBinder switches a mission-scoped Matrix client to the Human
// administrator observed from the official AgentTeams topology.
type HumanMatrixBinder interface {
	BindHuman(context.Context, HumanMatrixIdentity) error
}

type HumanMatrixIdentity struct {
	Name        string
	UID         string
	PrincipalID string
}

type KubernetesHumanMatrixClient struct {
	kubernetes dynamic.Interface
	namespace  string
	config     MatrixV3Config
	client     *MatrixV3Client
}

func NewKubernetesHumanMatrixClient(kubernetes dynamic.Interface, namespace string, config MatrixV3Config) (*KubernetesHumanMatrixClient, error) {
	namespace = strings.TrimSpace(namespace)
	if kubernetes == nil || namespace == "" {
		return nil, errors.New("Kubernetes Human Matrix client requires a dynamic client and namespace")
	}
	config.Username, config.Password, config.AccessToken, config.ExpectedUserID = "", "", "", ""
	if strings.TrimSpace(config.AppServiceToken) == "" {
		return nil, errors.New("Kubernetes Human Matrix client requires an AppService token")
	}
	if _, err := NewMatrixV3Client(config); err != nil {
		return nil, err
	}
	return &KubernetesHumanMatrixClient{kubernetes: kubernetes, namespace: namespace, config: config}, nil
}

func (client *KubernetesHumanMatrixClient) BindHuman(ctx context.Context, identity HumanMatrixIdentity) error {
	identity.Name = strings.TrimSpace(identity.Name)
	identity.UID = strings.TrimSpace(identity.UID)
	identity.PrincipalID = strings.TrimSpace(identity.PrincipalID)
	if client == nil || identity.Name == "" || identity.UID == "" || identity.PrincipalID == "" {
		return errors.New("official AgentTeams Human name is required")
	}
	human, err := client.kubernetes.Resource(officialHumanGVR).Namespace(client.namespace).Get(ctx, identity.Name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	phase, _, _ := unstructuredString(human.Object, "status", "phase")
	username, _, _ := unstructuredString(human.Object, "spec", "username")
	principal, _, _ := unstructuredString(human.Object, "status", "matrixUserID")
	if human.GetDeletionTimestamp() != nil || string(human.GetUID()) != identity.UID || principal != identity.PrincipalID || phase != "Active" || username == "" || !strings.HasPrefix(principal, "@"+username+":") {
		return errors.New("official AgentTeams Human Matrix identity is not ready")
	}
	config := client.config
	client.config.AppServiceToken = ""
	config.Username, config.ExpectedUserID = username, principal
	bound, err := NewMatrixV3Client(config)
	if err != nil {
		return err
	}
	if err := bound.Login(ctx); err != nil {
		return err
	}
	client.client = bound
	return nil
}

func (client *KubernetesHumanMatrixClient) SelectRoom(roomID string) error {
	if client == nil || client.client == nil {
		return errors.New("official AgentTeams Human Matrix identity is not bound")
	}
	return client.client.SelectRoom(roomID)
}

func (client *KubernetesHumanMatrixClient) Send(ctx context.Context, roomID string, outbound MatrixOutbound) error {
	if client == nil || client.client == nil {
		return errors.New("official AgentTeams Human Matrix identity is not bound")
	}
	return client.client.Send(ctx, roomID, outbound)
}

func (client *KubernetesHumanMatrixClient) Checkpoint(ctx context.Context) (string, error) {
	if client == nil || client.client == nil {
		return "", errors.New("official AgentTeams Human Matrix identity is not bound")
	}
	return client.client.Checkpoint(ctx)
}

func (client *KubernetesHumanMatrixClient) Sync(ctx context.Context, cursor string) (MatrixPage, error) {
	if client == nil || client.client == nil {
		return MatrixPage{}, errors.New("official AgentTeams Human Matrix identity is not bound")
	}
	return client.client.Sync(ctx, cursor)
}

func unstructuredString(object map[string]any, fields ...string) (string, bool, error) {
	value, found, err := unstructured.NestedString(object, fields...)
	return strings.TrimSpace(value), found, err
}
