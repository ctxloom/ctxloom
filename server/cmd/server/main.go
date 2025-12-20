// Package main implements the fragments server.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/reflection"

	"mlcm/server/middleware"
	pb "mlcm/server/proto/fragmentspb"
	"mlcm/server/service"
	"mlcm/server/storage"
)

var (
	grpcPort    = flag.Int("grpc-port", 50051, "gRPC server port")
	httpPort    = flag.Int("http-port", 8080, "HTTP/REST server port")
	storageType = flag.String("storage", "firestore", "Storage backend: firestore, mongodb, or dynamodb")

	// Firestore config
	gcpProject = flag.String("gcp-project", "", "GCP project ID (for Firestore)")

	// MongoDB config
	mongoURI = flag.String("mongo-uri", "", "MongoDB connection URI")
	mongoDB  = flag.String("mongo-db", "mlcm", "MongoDB database name")

	// DynamoDB config
	dynamoRegion = flag.String("dynamo-region", "", "AWS region for DynamoDB")

	// Rate limiting
	enableRateLimit = flag.Bool("rate-limit", true, "Enable rate limiting")
)

func main() {
	flag.Parse()

	// Override with environment variables if set
	if v := os.Getenv("GRPC_PORT"); v != "" {
		fmt.Sscanf(v, "%d", grpcPort)
	}
	if v := os.Getenv("HTTP_PORT"); v != "" {
		fmt.Sscanf(v, "%d", httpPort)
	}
	if v := os.Getenv("STORAGE_TYPE"); v != "" {
		*storageType = v
	}
	if v := os.Getenv("GCP_PROJECT"); v != "" {
		*gcpProject = v
	}
	if v := os.Getenv("MONGO_URI"); v != "" {
		*mongoURI = v
	}
	if v := os.Getenv("MONGO_DB"); v != "" {
		*mongoDB = v
	}
	if v := os.Getenv("AWS_REGION"); v != "" {
		*dynamoRegion = v
	}
	if v := os.Getenv("RATE_LIMIT"); v == "false" || v == "0" {
		*enableRateLimit = false
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Initialize storage
	store, err := initStorage(ctx)
	if err != nil {
		log.Fatalf("Failed to initialize storage: %v", err)
	}
	defer store.Close()

	// Create middleware
	var grpcOpts []grpc.ServerOption
	var rateLimiter *middleware.RateLimiter

	if *enableRateLimit {
		rateLimiter = middleware.NewRateLimiter(middleware.DefaultRateLimitConfig())
		defer rateLimiter.Stop()

		validator := middleware.NewValidator(middleware.DefaultValidationConfig())

		grpcOpts = append(grpcOpts,
			grpc.ChainUnaryInterceptor(
				rateLimiter.GRPCUnaryInterceptor(),
				validator.GRPCUnaryInterceptor(),
			),
		)
		log.Println("Rate limiting enabled")
	}

	// Create gRPC server
	grpcServer := grpc.NewServer(grpcOpts...)
	fragmentService := service.NewFragmentService(store)
	pb.RegisterFragmentServiceServer(grpcServer, fragmentService)
	reflection.Register(grpcServer)

	// Start gRPC server
	grpcAddr := fmt.Sprintf(":%d", *grpcPort)
	grpcListener, err := net.Listen("tcp", grpcAddr)
	if err != nil {
		log.Fatalf("Failed to listen on %s: %v", grpcAddr, err)
	}

	go func() {
		log.Printf("gRPC server listening on %s", grpcAddr)
		if err := grpcServer.Serve(grpcListener); err != nil {
			log.Fatalf("gRPC server failed: %v", err)
		}
	}()

	// Start REST gateway
	mux := runtime.NewServeMux()
	opts := []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())}

	err = pb.RegisterFragmentServiceHandlerFromEndpoint(ctx, mux, grpcAddr, opts)
	if err != nil {
		log.Fatalf("Failed to register gateway: %v", err)
	}

	// Apply rate limiting middleware to HTTP gateway
	var httpHandler http.Handler = mux
	if *enableRateLimit && rateLimiter != nil {
		httpHandler = rateLimiter.HTTPMiddleware(mux)
	}

	httpAddr := fmt.Sprintf(":%d", *httpPort)
	httpServer := &http.Server{
		Addr:    httpAddr,
		Handler: httpHandler,
	}

	go func() {
		log.Printf("REST gateway listening on %s", httpAddr)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("REST gateway failed: %v", err)
		}
	}()

	// Wait for shutdown signal
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	log.Println("Shutting down...")
	grpcServer.GracefulStop()
	httpServer.Shutdown(ctx)
}

func initStorage(ctx context.Context) (storage.Store, error) {
	switch *storageType {
	case "firestore":
		if *gcpProject == "" {
			return nil, fmt.Errorf("GCP project ID required for Firestore (use -gcp-project or GCP_PROJECT)")
		}
		return storage.NewFirestoreStore(ctx, storage.FirestoreConfig{
			ProjectID: *gcpProject,
		})

	case "mongodb":
		if *mongoURI == "" {
			return nil, fmt.Errorf("MongoDB URI required (use -mongo-uri or MONGO_URI)")
		}
		return storage.NewMongoStore(ctx, storage.MongoConfig{
			URI:      *mongoURI,
			Database: *mongoDB,
		})

	case "dynamodb":
		return storage.NewDynamoStore(ctx, storage.DynamoConfig{
			Region:         *dynamoRegion,
			FragmentsTable: os.Getenv("DYNAMODB_FRAGMENTS_TABLE"),
			DislikesTable:  os.Getenv("DYNAMODB_DISLIKES_TABLE"),
			EndpointURL:    os.Getenv("DYNAMODB_ENDPOINT"),
		})

	default:
		return nil, fmt.Errorf("unknown storage type: %s", *storageType)
	}
}
