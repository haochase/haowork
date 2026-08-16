package agentteamsbridge_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/haochase/haowork/internal/agentteamsbridge"
)

func TestHigressRequiresExpectedConsumerRouteAndMCPBinding(t *testing.T) {
	requests := make([]string, 0, 4)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		requests = append(requests, request.Method+" "+request.URL.Path)
		switch request.URL.Path {
		case "/session/login":
			if request.Method != http.MethodPost {
				t.Fatalf("login method = %s", request.Method)
			}
			response.Header().Add("Set-Cookie", "higress-session=opaque; Path=/; HttpOnly")
			_, _ = response.Write([]byte(`{"success":true}`))
		case "/v1/consumers":
			requireHigressSession(t, request)
			_, _ = response.Write([]byte(`{"data":[{"name":"worker-build"}]}`))
		case "/v1/routes":
			requireHigressSession(t, request)
			_, _ = response.Write([]byte(`{"data":[{"name":"haowork-mcp-route","authConfig":{"allowedConsumers":["worker-build"]}}]}`))
		case "/v1/mcpServer":
			requireHigressSession(t, request)
			_, _ = response.Write([]byte(`{"data":[{"name":"haowork-mcp"}]}`))
		case "/v1/mcpServer/consumers":
			requireHigressSession(t, request)
			if request.URL.Query().Get("mcpServerName") != "haowork-mcp" || request.URL.Query().Get("consumerName") != "worker-build" {
				t.Fatalf("MCP consumer query = %q", request.URL.RawQuery)
			}
			_, _ = response.Write([]byte(`{"total":1,"data":[{"mcpServerName":"haowork-mcp-route","consumerName":"worker-build"}]}`))
		default:
			response.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	inspector, err := agentteamsbridge.NewHigressInspector(agentteamsbridge.HigressConfig{
		ConsoleURL: server.URL, Username: "admin", Password: "console-secret", Client: server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	inspection, err := inspector.Inspect(context.Background(), agentteamsbridge.HigressExpectation{
		ConsumerName: "worker-build", RouteName: "haowork-mcp-route", MCPServerName: "haowork-mcp",
	})
	if err != nil {
		t.Fatal(err)
	}
	if inspection.ConsumerName != "worker-build" || inspection.RouteName != "haowork-mcp-route" || inspection.MCPServerName != "haowork-mcp" {
		t.Fatalf("inspection = %#v", inspection)
	}
	if len(requests) != 5 {
		t.Fatalf("Higress requests = %#v", requests)
	}
}

func TestHigressNeverReturnsConsumerSecretToBrowserOrTrace(t *testing.T) {
	const secret = "consumer-secret-must-not-leak"
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/session/login":
			response.Header().Add("Set-Cookie", "higress-session=opaque; Path=/; HttpOnly")
			_, _ = response.Write([]byte(`{"success":true}`))
		case "/v1/consumers":
			_, _ = response.Write([]byte(`{"data":[{"name":"worker-build","credentials":[{"values":["` + secret + `"]}]}]}`))
		case "/v1/routes":
			_, _ = response.Write([]byte(`{"data":[{"name":"haowork-mcp-route","authConfig":{"allowedConsumers":["worker-build"]}}]}`))
		case "/v1/mcpServer":
			_, _ = response.Write([]byte(`{"data":[{"name":"haowork-mcp"}]}`))
		case "/v1/mcpServer/consumers":
			_, _ = response.Write([]byte(`{"total":0,"data":[]}`))
		default:
			response.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	inspector, err := agentteamsbridge.NewHigressInspector(agentteamsbridge.HigressConfig{
		ConsoleURL: server.URL, Username: "admin", Password: "console-secret", Client: server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = inspector.Inspect(context.Background(), agentteamsbridge.HigressExpectation{
		ConsumerName: "worker-build", RouteName: "haowork-mcp-route", MCPServerName: "haowork-mcp",
	})
	if err == nil {
		t.Fatal("Higress inspector accepted a missing MCP binding")
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "console-secret") {
		t.Fatalf("Higress error leaks a secret: %v", err)
	}
	inspection := agentteamsbridge.HigressInspection{ConsumerName: "worker-build", RouteName: "haowork-mcp-route", MCPServerName: "haowork-mcp"}
	encoded, err := json.Marshal(inspection)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), secret) || strings.Contains(string(encoded), "console-secret") {
		t.Fatalf("browser/trace inspection leaks a secret: %s", encoded)
	}
}

func TestHigressRejectsConsoleURLWithUserinfo(t *testing.T) {
	_, err := agentteamsbridge.NewHigressInspector(agentteamsbridge.HigressConfig{
		ConsoleURL: "https://operator:password@higress.example.test", Username: "admin", Password: "console-secret",
	})
	if err == nil {
		t.Fatal("Higress inspector accepted Console URL userinfo")
	}
}

func TestHigressAllowsOnlyExplicitClusterLocalHTTP(t *testing.T) {
	if _, err := agentteamsbridge.NewHigressInspector(agentteamsbridge.HigressConfig{
		ConsoleURL: "http://higress-console.haowork-public.svc.cluster.local:8080", Username: "admin", Password: "secret", AllowInsecureClusterLocal: true,
	}); err != nil {
		t.Fatalf("cluster-local Higress URL rejected: %v", err)
	}
	if _, err := agentteamsbridge.NewHigressInspector(agentteamsbridge.HigressConfig{
		ConsoleURL: "http://higress.example.test", Username: "admin", Password: "secret", AllowInsecureClusterLocal: true,
	}); err == nil {
		t.Fatal("arbitrary insecure Higress URL was accepted")
	}
}

func TestHigressAcceptsOfficialResponseExtensions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/session/login":
			response.Header().Add("Set-Cookie", "higress-session=opaque; Path=/; HttpOnly")
			_, _ = response.Write([]byte(`{"success":true,"message":"ok","session":{"id":"ignored"}}`))
		case "/v1/consumers":
			_, _ = response.Write([]byte(`{"success":true,"code":200,"message":"ok","data":[{"name":"worker-build","credentials":[{"type":"key-auth","key":"not-trusted"}]}],"total":1,"pageNum":0,"pageSize":20,"future":"ignored"}`))
		case "/v1/routes":
			_, _ = response.Write([]byte(`{"success":true,"message":"ok","data":[{"name":"haowork-mcp-route","version":"1","domains":["example.test"],"authConfig":{"allowedConsumers":["worker-build"],"other":"ignored"}}],"total":1,"pageNum":0,"pageSize":20}`))
		case "/v1/mcpServer":
			_, _ = response.Write([]byte(`{"success":true,"message":"ok","data":[{"name":"haowork-mcp","future":"ignored"}],"total":1}`))
		case "/v1/mcpServer/consumers":
			_, _ = response.Write([]byte(`{"success":true,"message":"ok","total":1,"pageNum":0,"pageSize":20,"data":[{"mcpServerName":"haowork-mcp-route","consumerName":"worker-build","future":"ignored"}]}`))
		default:
			response.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	inspector, err := agentteamsbridge.NewHigressInspector(agentteamsbridge.HigressConfig{
		ConsoleURL: server.URL, Username: "admin", Password: "console-secret", Client: server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	inspection, err := inspector.Inspect(context.Background(), agentteamsbridge.HigressExpectation{
		ConsumerName: "worker-build", RouteName: "haowork-mcp-route", MCPServerName: "haowork-mcp",
	})
	if err != nil || inspection.ConsumerName != "worker-build" {
		t.Fatalf("official Higress extensions result = %#v, err=%v", inspection, err)
	}
}

func TestHigressRejectsMalformedResponseFields(t *testing.T) {
	for name, consumers := range map[string]string{
		"malicious type":     `{"success":"yes","data":[{"name":"worker-build"}]}`,
		"multiple documents": `{"success":true,"data":[{"name":"worker-build"}]}{"success":true}`,
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				switch request.URL.Path {
				case "/session/login":
					response.Header().Add("Set-Cookie", "higress-session=opaque; Path=/; HttpOnly")
					_, _ = response.Write([]byte(`{"success":true}`))
				case "/v1/consumers":
					_, _ = response.Write([]byte(consumers))
				default:
					response.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			inspector, err := agentteamsbridge.NewHigressInspector(agentteamsbridge.HigressConfig{ConsoleURL: server.URL, Username: "admin", Password: "console-secret", Client: server.Client()})
			if err != nil {
				t.Fatal(err)
			}
			_, err = inspector.Inspect(context.Background(), agentteamsbridge.HigressExpectation{ConsumerName: "worker-build", RouteName: "haowork-mcp-route", MCPServerName: "haowork-mcp"})
			if err == nil {
				t.Fatalf("Higress accepted malformed %s response", name)
			}
		})
	}
}

func TestHigressRejectsNonSuccessBusinessCode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/session/login":
			response.Header().Add("Set-Cookie", "higress-session=opaque; Path=/; HttpOnly")
			_, _ = response.Write([]byte(`{"success":true}`))
		case "/v1/consumers":
			_, _ = response.Write([]byte(`{"success":true,"code":500,"data":[{"name":"worker-build"}],"total":1}`))
		case "/v1/routes":
			_, _ = response.Write([]byte(`{"success":true,"data":[{"name":"haowork-mcp-route","authConfig":{"allowedConsumers":["worker-build"]}}]}`))
		case "/v1/mcpServer/consumers":
			_, _ = response.Write([]byte(`{"success":true,"data":[{"mcpServerName":"haowork-mcp","consumerName":"worker-build"}]}`))
		default:
			response.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	inspector, err := agentteamsbridge.NewHigressInspector(agentteamsbridge.HigressConfig{ConsoleURL: server.URL, Username: "admin", Password: "console-secret", Client: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	_, err = inspector.Inspect(context.Background(), agentteamsbridge.HigressExpectation{ConsumerName: "worker-build", RouteName: "haowork-mcp-route", MCPServerName: "haowork-mcp"})
	if err == nil {
		t.Fatal("Higress inspector accepted a non-success business code")
	}
}

func requireHigressSession(t *testing.T, request *http.Request) {
	t.Helper()
	if _, err := request.Cookie("higress-session"); err != nil {
		t.Fatalf("Higress request missing session cookie: %v", err)
	}
}
