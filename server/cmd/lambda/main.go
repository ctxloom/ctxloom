// Package main implements an AWS Lambda handler for the fragment service.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"

	"mlcm/server/service"
	"mlcm/server/storage"

	pb "mlcm/server/proto/fragmentspb"
)

var svc *service.FragmentService

func init() {
	ctx := context.Background()

	// Initialize storage based on environment
	storageType := os.Getenv("STORAGE_TYPE")
	if storageType == "" {
		storageType = "dynamodb" // Default to DynamoDB for Lambda
	}

	store, err := initStorage(ctx, storageType)
	if err != nil {
		log.Fatalf("Failed to initialize storage: %v", err)
	}

	svc = service.NewFragmentService(store)
}

func initStorage(ctx context.Context, storageType string) (storage.Store, error) {
	switch storageType {
	case "dynamodb":
		cfg := storage.DynamoConfig{
			Region:         os.Getenv("AWS_REGION"),
			FragmentsTable: os.Getenv("DYNAMODB_FRAGMENTS_TABLE"),
			DislikesTable:  os.Getenv("DYNAMODB_DISLIKES_TABLE"),
			EndpointURL:    os.Getenv("DYNAMODB_ENDPOINT"),
		}
		return storage.NewDynamoStore(ctx, cfg)
	case "mongodb":
		cfg := storage.MongoConfig{
			URI:        os.Getenv("MONGODB_URI"),
			Database:   os.Getenv("MONGODB_DATABASE"),
			Collection: os.Getenv("MONGODB_COLLECTION"),
		}
		return storage.NewMongoStore(ctx, cfg)
	case "firestore":
		cfg := storage.FirestoreConfig{
			ProjectID:  os.Getenv("GCP_PROJECT_ID"),
			Collection: os.Getenv("FIRESTORE_COLLECTION"),
		}
		return storage.NewFirestoreStore(ctx, cfg)
	default:
		return nil, fmt.Errorf("unknown storage type: %s", storageType)
	}
}

func handler(ctx context.Context, request events.APIGatewayProxyRequest) (events.APIGatewayProxyResponse, error) {
	path := request.Path
	method := request.HTTPMethod

	// Route to appropriate handler
	switch {
	case method == "GET" && path == "/v1/fragments":
		return handleListFragments(ctx, request)
	case method == "POST" && path == "/v1/fragments":
		return handleCreateFragment(ctx, request)
	case method == "GET" && strings.HasPrefix(path, "/v1/fragments/search"):
		return handleSearchFragments(ctx, request)
	case method == "GET" && strings.HasPrefix(path, "/v1/fragments/by-name/"):
		return handleGetFragmentByName(ctx, request)
	case method == "GET" && strings.HasPrefix(path, "/v1/fragments/") && strings.HasSuffix(path, "/dislikes"):
		return handleListDislikes(ctx, request)
	case method == "POST" && strings.HasSuffix(path, "/download"):
		return handleDownloadFragment(ctx, request)
	case method == "POST" && strings.HasSuffix(path, "/like"):
		return handleLikeFragment(ctx, request)
	case method == "POST" && strings.HasSuffix(path, "/dislike"):
		return handleDislikeFragment(ctx, request)
	case method == "GET" && strings.HasPrefix(path, "/v1/fragments/"):
		return handleGetFragment(ctx, request)
	case method == "PUT" && strings.HasPrefix(path, "/v1/fragments/"):
		return handleUpdateFragment(ctx, request)
	case method == "DELETE" && strings.HasPrefix(path, "/v1/fragments/"):
		return handleDeleteFragment(ctx, request)
	default:
		return errorResponse(http.StatusNotFound, "Not found"), nil
	}
}

func handleListFragments(ctx context.Context, req events.APIGatewayProxyRequest) (events.APIGatewayProxyResponse, error) {
	params := req.QueryStringParameters
	listReq := &pb.ListFragmentsRequest{
		Author:     params["author"],
		NamePrefix: params["name_prefix"],
		PageToken:  params["page_token"],
	}
	if tags := params["tags"]; tags != "" {
		listReq.Tags = strings.Split(tags, ",")
	}

	resp, err := svc.ListFragments(ctx, listReq)
	if err != nil {
		return grpcErrorResponse(err), nil
	}
	return jsonResponse(http.StatusOK, resp)
}

func handleCreateFragment(ctx context.Context, req events.APIGatewayProxyRequest) (events.APIGatewayProxyResponse, error) {
	var createReq pb.CreateFragmentRequest
	if err := json.Unmarshal([]byte(req.Body), &createReq); err != nil {
		return errorResponse(http.StatusBadRequest, "Invalid JSON body"), nil
	}

	resp, err := svc.CreateFragment(ctx, &createReq)
	if err != nil {
		return grpcErrorResponse(err), nil
	}
	return jsonResponse(http.StatusCreated, resp)
}

func handleGetFragment(ctx context.Context, req events.APIGatewayProxyRequest) (events.APIGatewayProxyResponse, error) {
	id := extractID(req.Path, "/v1/fragments/")
	resp, err := svc.GetFragment(ctx, &pb.GetFragmentRequest{Id: id})
	if err != nil {
		return grpcErrorResponse(err), nil
	}
	return jsonResponse(http.StatusOK, resp)
}

func handleGetFragmentByName(ctx context.Context, req events.APIGatewayProxyRequest) (events.APIGatewayProxyResponse, error) {
	name := extractID(req.Path, "/v1/fragments/by-name/")
	version := req.QueryStringParameters["version"]
	resp, err := svc.GetFragmentByName(ctx, &pb.GetFragmentByNameRequest{Name: name, Version: version})
	if err != nil {
		return grpcErrorResponse(err), nil
	}
	return jsonResponse(http.StatusOK, resp)
}

func handleUpdateFragment(ctx context.Context, req events.APIGatewayProxyRequest) (events.APIGatewayProxyResponse, error) {
	id := extractID(req.Path, "/v1/fragments/")
	var updateReq pb.UpdateFragmentRequest
	if err := json.Unmarshal([]byte(req.Body), &updateReq); err != nil {
		return errorResponse(http.StatusBadRequest, "Invalid JSON body"), nil
	}
	updateReq.Id = id

	resp, err := svc.UpdateFragment(ctx, &updateReq)
	if err != nil {
		return grpcErrorResponse(err), nil
	}
	return jsonResponse(http.StatusOK, resp)
}

func handleDeleteFragment(ctx context.Context, req events.APIGatewayProxyRequest) (events.APIGatewayProxyResponse, error) {
	id := extractID(req.Path, "/v1/fragments/")
	_, err := svc.DeleteFragment(ctx, &pb.DeleteFragmentRequest{Id: id})
	if err != nil {
		return grpcErrorResponse(err), nil
	}
	return events.APIGatewayProxyResponse{StatusCode: http.StatusNoContent}, nil
}

func handleSearchFragments(ctx context.Context, req events.APIGatewayProxyRequest) (events.APIGatewayProxyResponse, error) {
	params := req.QueryStringParameters
	searchReq := &pb.SearchFragmentsRequest{
		Query:     params["query"],
		PageToken: params["page_token"],
	}
	if tags := params["tags"]; tags != "" {
		searchReq.Tags = strings.Split(tags, ",")
	}

	resp, err := svc.SearchFragments(ctx, searchReq)
	if err != nil {
		return grpcErrorResponse(err), nil
	}
	return jsonResponse(http.StatusOK, resp)
}

func handleDownloadFragment(ctx context.Context, req events.APIGatewayProxyRequest) (events.APIGatewayProxyResponse, error) {
	id := extractFragmentID(req.Path, "/download")
	resp, err := svc.DownloadFragment(ctx, &pb.DownloadFragmentRequest{Id: id})
	if err != nil {
		return grpcErrorResponse(err), nil
	}
	return jsonResponse(http.StatusOK, resp)
}

func handleLikeFragment(ctx context.Context, req events.APIGatewayProxyRequest) (events.APIGatewayProxyResponse, error) {
	id := extractFragmentID(req.Path, "/like")
	resp, err := svc.LikeFragment(ctx, &pb.LikeFragmentRequest{Id: id})
	if err != nil {
		return grpcErrorResponse(err), nil
	}
	return jsonResponse(http.StatusOK, resp)
}

func handleDislikeFragment(ctx context.Context, req events.APIGatewayProxyRequest) (events.APIGatewayProxyResponse, error) {
	id := extractFragmentID(req.Path, "/dislike")
	var dislikeReq struct {
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(req.Body), &dislikeReq); err != nil {
		return errorResponse(http.StatusBadRequest, "Invalid JSON body"), nil
	}

	resp, err := svc.DislikeFragment(ctx, &pb.DislikeFragmentRequest{Id: id, Reason: dislikeReq.Reason})
	if err != nil {
		return grpcErrorResponse(err), nil
	}
	return jsonResponse(http.StatusOK, resp)
}

func handleListDislikes(ctx context.Context, req events.APIGatewayProxyRequest) (events.APIGatewayProxyResponse, error) {
	// Extract fragment ID from path like /v1/fragments/{id}/dislikes
	path := strings.TrimPrefix(req.Path, "/v1/fragments/")
	path = strings.TrimSuffix(path, "/dislikes")
	id := path

	resp, err := svc.ListDislikes(ctx, &pb.ListDislikesRequest{FragmentId: id})
	if err != nil {
		return grpcErrorResponse(err), nil
	}
	return jsonResponse(http.StatusOK, resp)
}

// Helper functions

func extractID(path, prefix string) string {
	id := strings.TrimPrefix(path, prefix)
	// Remove any trailing path components
	if idx := strings.Index(id, "/"); idx != -1 {
		id = id[:idx]
	}
	return id
}

func extractFragmentID(path, suffix string) string {
	path = strings.TrimSuffix(path, suffix)
	return extractID(path, "/v1/fragments/")
}

func jsonResponse(statusCode int, data interface{}) (events.APIGatewayProxyResponse, error) {
	body, err := json.Marshal(data)
	if err != nil {
		return errorResponse(http.StatusInternalServerError, "Failed to serialize response"), nil
	}
	return events.APIGatewayProxyResponse{
		StatusCode: statusCode,
		Headers:    map[string]string{"Content-Type": "application/json"},
		Body:       string(body),
	}, nil
}

func errorResponse(statusCode int, message string) events.APIGatewayProxyResponse {
	body, _ := json.Marshal(map[string]string{"error": message})
	return events.APIGatewayProxyResponse{
		StatusCode: statusCode,
		Headers:    map[string]string{"Content-Type": "application/json"},
		Body:       string(body),
	}
}

func grpcErrorResponse(err error) events.APIGatewayProxyResponse {
	// Extract gRPC status code and convert to HTTP
	// For now, treat all errors as internal server errors
	// In production, parse grpc/status codes properly
	return errorResponse(http.StatusInternalServerError, err.Error())
}

func main() {
	lambda.Start(handler)
}
