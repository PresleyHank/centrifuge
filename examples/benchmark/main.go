package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	_ "net/http/pprof"

	"github.com/centrifugal/centrifuge"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"google.golang.org/grpc"
)

func handleLog(e centrifuge.LogEntry) {
	log.Printf("%s: %+v", e.Message, e.Fields)
}

func httpAuthMiddleware(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		newCtx := centrifuge.SetCredentials(ctx, &centrifuge.Credentials{
			UserID: "42",
		})
		r = r.WithContext(newCtx)
		h.ServeHTTP(w, r)
	})
}

func grpcAuthInterceptor(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	// You probably want to authenticate user by information included in stream metadata.
	// meta, ok := metadata.FromIncomingContext(ss.Context())
	// But here we skip it for simplicity and just always authenticate user with ID 42.
	ctx := ss.Context()
	newCtx := centrifuge.SetCredentials(ctx, &centrifuge.Credentials{
		UserID: "42",
	})

	// GRPC has no builtin method to add data to context so here we use small
	// wrapper over ServerStream.
	wrapped := WrapServerStream(ss)
	wrapped.WrappedContext = newCtx
	return handler(srv, wrapped)
}

func waitExitSignal(n *centrifuge.Node) {
	sigs := make(chan os.Signal, 1)
	done := make(chan bool, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigs
		n.Shutdown()
		done <- true
	}()
	<-done
}

var dataBytes []byte

func init() {
	data := map[string]interface{}{
		"_id":        "5adece493c1a23736b037c52",
		"isActive":   false,
		"balance":    "$2,199.02",
		"picture":    "http://placehold.it/32x32",
		"age":        25,
		"eyeColor":   "blue",
		"name":       "Swanson Walker",
		"gender":     "male",
		"company":    "SHADEASE",
		"email":      "swansonwalker@shadease.com",
		"phone":      "+1 (885) 410-3991",
		"address":    "768 Paerdegat Avenue, Gouglersville, Oklahoma, 5380",
		"registered": "2016-01-24T07:40:09 -03:00",
		"latitude":   -71.336378,
		"longitude":  -28.155956,
		"tags": []string{
			"magna",
			"nostrud",
			"irure",
			"aliquip",
			"culpa",
			"sint",
		},
		"greeting":      "Hello, Swanson Walker! You have 9 unread messages.",
		"favoriteFruit": "apple",
	}

	var err error
	dataBytes, err = json.Marshal(data)
	if err != nil {
		panic(err.Error())
	}
}

func main() {
	cfg := centrifuge.DefaultConfig
	cfg.Publish = true

	node, _ := centrifuge.New(cfg)

	node.On().Connect(func(ctx context.Context, client *centrifuge.Client, e centrifuge.ConnectEvent) centrifuge.ConnectReply {

		client.On().Subscribe(func(e centrifuge.SubscribeEvent) centrifuge.SubscribeReply {
			log.Printf("user %s subscribes on %s", client.UserID(), e.Channel)
			return centrifuge.SubscribeReply{}
		})

		client.On().Unsubscribe(func(e centrifuge.UnsubscribeEvent) centrifuge.UnsubscribeReply {
			// log.Printf("user %s unsubscribed from %s", client.UserID(), e.Channel)
			return centrifuge.UnsubscribeReply{}
		})

		client.On().Publish(func(e centrifuge.PublishEvent) centrifuge.PublishReply {
			// Do not log here - lots of publications expected.
			return centrifuge.PublishReply{}
		})

		client.On().Message(func(e centrifuge.MessageEvent) centrifuge.MessageReply {
			// Do not log here - lots of messages expected.
			err := client.Send(dataBytes)
			if err != nil {
				if err != io.EOF {
					log.Fatalln("error senfing to client:", err.Error())
				}
			}
			return centrifuge.MessageReply{}
		})

		client.On().Disconnect(func(e centrifuge.DisconnectEvent) centrifuge.DisconnectReply {
			log.Printf("user %s disconnected", client.UserID())
			return centrifuge.DisconnectReply{}
		})

		log.Printf("user %s connected via %s with encoding: %s", client.UserID(), client.Transport().Name(), client.Transport().Encoding())
		return centrifuge.ConnectReply{}
	})

	node.SetLogHandler(centrifuge.LogLevelError, handleLog)

	if err := node.Run(); err != nil {
		panic(err)
	}

	http.Handle("/connection/websocket", httpAuthMiddleware(centrifuge.NewWebsocketHandler(node, centrifuge.WebsocketConfig{})))
	http.Handle("/metrics", promhttp.Handler())

	go func() {
		if err := http.ListenAndServe(":8000", nil); err != nil {
			panic(err)
		}
	}()

	grpcServer := grpc.NewServer(
		grpc.StreamInterceptor(grpcAuthInterceptor),
	)
	centrifuge.RegisterGRPCServerClient(node, grpcServer, centrifuge.GRPCClientServiceConfig{})
	go func() {
		listener, _ := net.Listen("tcp", ":8001")
		if err := grpcServer.Serve(listener); err != nil {
			log.Fatalf("Serve GRPC: %v", err)
		}
	}()

	waitExitSignal(node)
	fmt.Println("exiting")
}

// WrappedServerStream is a thin wrapper around grpc.ServerStream that allows modifying context.
// This can be replaced to analogue from github.com/grpc-ecosystem/go-grpc-middleware package -
// https://github.com/grpc-ecosystem/go-grpc-middleware/blob/master/wrappers.go –
// you most probably will have dependency to it in your application as it has lots of useful
// features to deal with GRPC.
type WrappedServerStream struct {
	grpc.ServerStream
	// WrappedContext is the wrapper's own Context. You can assign it.
	WrappedContext context.Context
}

// Context returns the wrapper's WrappedContext, overwriting the nested grpc.ServerStream.Context()
func (w *WrappedServerStream) Context() context.Context {
	return w.WrappedContext
}

// WrapServerStream returns a ServerStream that has the ability to overwrite context.
func WrapServerStream(stream grpc.ServerStream) *WrappedServerStream {
	if existing, ok := stream.(*WrappedServerStream); ok {
		return existing
	}
	return &WrappedServerStream{ServerStream: stream, WrappedContext: stream.Context()}
}
