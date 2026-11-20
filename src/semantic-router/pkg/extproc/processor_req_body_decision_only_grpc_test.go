package extproc

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	core "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	ext_proc "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

func TestDecisionOnlyGRPCRequestStream(t *testing.T) {
	for _, tc := range []struct {
		name, model, content, wantModel, wantMethod string
		stream, looper, noEndpoints                 bool
	}{
		{name: "rule", model: "auto", content: "coding", wantModel: "Pro", wantMethod: "routing"},
		{name: "fallback", model: "auto", content: "hello", wantModel: "Fast", wantMethod: "fallback"},
		{name: "explicit", model: "Pro", content: "hello", wantModel: "Pro", wantMethod: "passthrough"},
		{name: "stream request still returns decision JSON", model: "auto", content: "coding", stream: true, wantModel: "Pro", wantMethod: "routing"},
		{name: "looper opt out preserved", model: "Pro", content: "hello", looper: true},
		{name: "type without backend", model: "auto", content: "coding", noEndpoints: true, wantModel: "Pro", wantMethod: "routing"},
		{name: "default type without backend", model: "auto", content: "hello", noEndpoints: true, wantModel: "Fast", wantMethod: "fallback"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			router := newDecisionOnlyTestRouter(t)
			if tc.noEndpoints {
				router.Config.VLLMEndpoints = nil
			}
			prepareDecisionOnlyFlow(t, router)
			listener := bufconn.Listen(1024 * 1024)
			server := grpc.NewServer()
			ext_proc.RegisterExternalProcessorServer(server, router)
			t.Cleanup(func() { server.Stop(); _ = listener.Close() })
			go func() { _ = server.Serve(listener) }()
			conn, err := grpc.NewClient("passthrough:///decision-only-test",
				grpc.WithTransportCredentials(insecure.NewCredentials()),
				grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }),
			)
			require.NoError(t, err)
			t.Cleanup(func() { _ = conn.Close() })
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			stream, err := ext_proc.NewExternalProcessorClient(conn).Process(ctx)
			require.NoError(t, err)
			headers := []*core.HeaderValue{
				{Key: ":method", Value: "POST"}, {Key: ":path", Value: "/v1/chat/completions"},
				{Key: "content-type", Value: "application/json"},
			}
			if tc.looper {
				headers = append(headers, &core.HeaderValue{Key: "x-vsr-looper-request", Value: "true"})
			}
			require.NoError(t, stream.Send(&ext_proc.ProcessingRequest{Request: &ext_proc.ProcessingRequest_RequestHeaders{
				RequestHeaders: &ext_proc.HttpHeaders{Headers: &core.HeaderMap{Headers: headers}},
			}}))
			response, err := stream.Recv()
			require.NoError(t, err)
			require.NotNil(t, response.GetRequestHeaders())
			body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":%q}],"stream":%v}`, tc.model, tc.content, tc.stream)
			require.NoError(t, stream.Send(&ext_proc.ProcessingRequest{Request: decisionOnlyBodyRequest(body)}))
			response, err = stream.Recv()
			require.NoError(t, err)
			if tc.looper {
				require.Nil(t, response.GetImmediateResponse())
				require.NotNil(t, response.GetRequestBody())
			} else {
				require.NotNil(t, response.GetImmediateResponse())
				require.EqualValues(t, 200, response.GetImmediateResponse().GetStatus().GetCode())
				var payload selectedModelDecisionPayload
				require.NoError(t, json.Unmarshal(response.GetImmediateResponse().GetBody(), &payload))
				require.Equal(t, tc.wantModel, payload.SelectedModel)
				require.Equal(t, tc.wantMethod, payload.SelectionMethod)
			}
			require.NoError(t, stream.CloseSend())
			_, err = stream.Recv()
			require.ErrorIs(t, err, io.EOF)
		})
	}
}
