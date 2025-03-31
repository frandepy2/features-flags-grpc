package main

import (
	"context"
	"fmt"
	"log"

	pb "github.com/frandepy2/featureflags-grpc/proto"
	"google.golang.org/grpc"
)

func main() {
	conn, err := grpc.Dial("localhost:50051", grpc.WithInsecure())
	if err != nil {
		log.Fatalf("No se pudo conectar al servidor gRPC: %v", err)
	}
	defer conn.Close()

	client := pb.NewFeatureFlagsClient(conn)

	stream, err := client.WatchFeature(context.Background(), &pb.FeatureRequest{
		FeatureKey: "welcome_message",
		UserId:     "user123",
	})

	if err != nil {
		log.Fatalf("Error al suscribirse al stream: %v", err)
	}

	log.Println("📡 Escuchando cambios del flag dark_mode...")

	for {
		resp, err := stream.Recv()
		if err != nil {
			log.Fatalf("Error recibiendo stream: %v", err)
		}
		fmt.Printf("🎯 Nuevo valor del flag: %v\n", resp.Value)
	}
}
