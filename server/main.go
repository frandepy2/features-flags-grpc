package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/frandepy2/featureflags-grpc/internal/storage"
	pb "github.com/frandepy2/featureflags-grpc/proto"
	"github.com/redis/go-redis/v9"
	"go.mongodb.org/mongo-driver/bson"
	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"
)

type featureFlagsServer struct {
	pb.UnimplementedFeatureFlagsServer
	redis  *storage.RedisClient
	mongo  *storage.MongoClient // Cliente MongoDB
	pubsub *redis.PubSub
}

func (s *featureFlagsServer) GetFeature(ctx context.Context, req *pb.FeatureRequest) (*pb.FeatureResponse, error) {
	log.Printf("GetFeature: key=%s, user_id=%s, app=%s, env=%s", req.FeatureKey, req.UserId, req.App, req.Env)

	key := "feature:" + req.App + ":" + req.Env + ":" + req.FeatureKey

	// Primero, intentamos obtener el valor desde Redis
	val, err := s.redis.Client.Get(ctx, key).Result()
	if err == redis.Nil {
		log.Println("⚠️ Flag no encontrado en Redis. Buscando en MongoDB...")

		// Si no lo encontramos en Redis, buscamos en MongoDB
		feature, mongoErr := s.mongo.GetFeature(ctx, key)
		if mongoErr != nil {
			log.Printf("❌ Error al obtener desde MongoDB: %v", mongoErr)
			return &pb.FeatureResponse{}, nil
		}

		// Devolvemos la respuesta desde MongoDB
		var resp *pb.FeatureResponse
		if feature["type"] == "bool" {
			b := feature["value"] == "1"
			resp = &pb.FeatureResponse{
				Value: &pb.FeatureValue{
					Value: &pb.FeatureValue_BoolValue{BoolValue: b},
				},
			}
		} else if feature["type"] == "string" {
			resp = &pb.FeatureResponse{
				Value: &pb.FeatureValue{
					Value: &pb.FeatureValue_StringValue{StringValue: feature["value"]},
				},
			}
		}
		return resp, nil
	} else if err != nil {
		log.Printf("❌ Error de Redis: %v", err)
		return &pb.FeatureResponse{}, nil
	}

	// Si lo encontramos en Redis, procesamos el valor
	var stored struct {
		Type  string `json:"type"`
		Value string `json:"value"`
	}

	if err := json.Unmarshal([]byte(val), &stored); err != nil {
		log.Printf("Error al parsear JSON desde Redis: %v", err)
		return &pb.FeatureResponse{}, nil
	}

	// Según el tipo, devolvemos el valor adecuado
	switch stored.Type {
	case "bool":
		b := stored.Value == "1"
		return &pb.FeatureResponse{
			Value: &pb.FeatureValue{
				Value: &pb.FeatureValue_BoolValue{BoolValue: b},
			},
		}, nil
	case "string":
		return &pb.FeatureResponse{
			Value: &pb.FeatureValue{
				Value: &pb.FeatureValue_StringValue{StringValue: stored.Value},
			},
		}, nil
	case "int":
		i, _ := strconv.Atoi(stored.Value)
		return &pb.FeatureResponse{
			Value: &pb.FeatureValue{
				Value: &pb.FeatureValue_IntValue{IntValue: int32(i)},
			},
		}, nil
	case "json":
		return &pb.FeatureResponse{
			Value: &pb.FeatureValue{
				Value: &pb.FeatureValue_JsonValue{JsonValue: stored.Value},
			},
		}, nil
	default:
		return &pb.FeatureResponse{}, nil
	}
}

func (s *featureFlagsServer) SetFeature(ctx context.Context, req *pb.FeatureConfig) (*pb.FeatureAck, error) {
	log.Printf("SetFeature: key=%s, app=%s, env=%s", req.FeatureKey, req.App, req.Env)

	// Construimos la clave Redis con app y env
	key := "feature:" + req.App + ":" + req.Env + ":" + req.FeatureKey

	// Determinamos el tipo de valor a guardar
	var storeType string
	var storeValue string

	switch v := req.Value.Value.(type) {
	case *pb.FeatureValue_BoolValue:
		storeType = "bool"
		if v.BoolValue {
			storeValue = "1"
		} else {
			storeValue = "0"
		}
	case *pb.FeatureValue_StringValue:
		storeType = "string"
		storeValue = v.StringValue
	case *pb.FeatureValue_IntValue:
		storeType = "int"
		storeValue = strconv.Itoa(int(v.IntValue))
	case *pb.FeatureValue_JsonValue:
		storeType = "json"
		storeValue = v.JsonValue
	default:
		log.Println("❌ Tipo de valor no soportado")
		return &pb.FeatureAck{Success: false}, nil
	}

	// Guardamos el flag en Redis
	payload, _ := json.Marshal(map[string]string{
		"type":  storeType,
		"value": storeValue,
	})

	err := s.redis.Client.Set(ctx, key, payload, 24*time.Hour).Err()
	if err != nil {
		log.Printf("Error al guardar en Redis: %v", err)
		return &pb.FeatureAck{Success: false}, err
	}

	// Guardamos el flag también en MongoDB
	mongoErr := s.mongo.SetFeature(ctx, key, map[string]string{
		"type":  storeType,
		"value": storeValue,
	})
	if mongoErr != nil {
		log.Printf("Error al guardar en MongoDB: %v", mongoErr)
		return &pb.FeatureAck{Success: false}, mongoErr
	}

	// Publicamos el cambio en Redis para notificar a los suscriptores
	err = s.redis.Client.Publish(ctx, key, payload).Err()
	if err != nil {
		log.Printf("Error al publicar en Redis: %v", err)
		return &pb.FeatureAck{Success: false}, err
	}

	return &pb.FeatureAck{Success: true}, nil
}

func (s *featureFlagsServer) WatchFeature(req *pb.FeatureRequest, stream pb.FeatureFlags_WatchFeatureServer) error {
	log.Printf("Cliente conectado para observar: %s, app=%s, env=%s", req.FeatureKey, req.App, req.Env)

	// Construir el canal Redis para el flag
	channel := "feature:" + req.App + ":" + req.Env + ":" + req.FeatureKey

	// Subscribirse al canal de Redis
	pubsub := s.redis.Client.Subscribe(context.Background(), channel)
	defer pubsub.Close()

	// Escuchar cambios
	for {
		// Recibimos el mensaje de Redis
		msg, err := pubsub.ReceiveMessage(context.Background())
		if err != nil {
			log.Printf("Error recibiendo mensaje de Redis: %v", err)
			return err
		}

		// Procesamos el mensaje
		var stored struct {
			Type  string `json:"type"`
			Value string `json:"value"`
		}
		if err := json.Unmarshal([]byte(msg.Payload), &stored); err != nil {
			log.Printf("Error al parsear el mensaje de Redis: %v", err)
			continue
		}

		// Devolvemos el valor
		var resp *pb.FeatureResponse
		switch stored.Type {
		case "bool":
			resp = &pb.FeatureResponse{
				Value: &pb.FeatureValue{
					Value: &pb.FeatureValue_BoolValue{BoolValue: stored.Value == "1"},
				},
			}
		case "string":
			resp = &pb.FeatureResponse{
				Value: &pb.FeatureValue{
					Value: &pb.FeatureValue_StringValue{StringValue: stored.Value},
				},
			}
		case "int":
			i, _ := strconv.Atoi(stored.Value)
			resp = &pb.FeatureResponse{
				Value: &pb.FeatureValue{
					Value: &pb.FeatureValue_IntValue{IntValue: int32(i)},
				},
			}
		case "json":
			resp = &pb.FeatureResponse{
				Value: &pb.FeatureValue{
					Value: &pb.FeatureValue_JsonValue{JsonValue: stored.Value},
				},
			}
		default:
			continue
		}

		// Enviar el valor actualizado al cliente
		log.Printf("🟢 Enviando nuevo valor: %+v", resp)
		stream.Send(resp)
	}

}
func mapStoredToFeatureValue(stored struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}) *pb.FeatureValue {
	switch stored.Type {
	case "bool":
		return &pb.FeatureValue{Value: &pb.FeatureValue_BoolValue{BoolValue: stored.Value == "1"}}
	case "string":
		return &pb.FeatureValue{Value: &pb.FeatureValue_StringValue{StringValue: stored.Value}}
	case "int":
		i, _ := strconv.Atoi(stored.Value)
		return &pb.FeatureValue{Value: &pb.FeatureValue_IntValue{IntValue: int32(i)}}
	case "json":
		return &pb.FeatureValue{Value: &pb.FeatureValue_JsonValue{JsonValue: stored.Value}}
	default:
		return nil
	}
}

func (s *featureFlagsServer) ListFlags(ctx context.Context, req *pb.FeatureQuery) (*pb.FeatureList, error) {
	prefix := fmt.Sprintf("feature:%s:%s:", req.App, req.Env)

	iter := s.redis.Client.Scan(ctx, 0, prefix+"*", 0).Iterator()
	var entries []*pb.FeatureEntry

	for iter.Next(ctx) {
		key := iter.Val()
		parts := strings.Split(key, ":")
		if len(parts) < 4 {
			continue
		}
		featureKey := parts[3]

		val, err := s.redis.Client.Get(ctx, key).Result()
		if err != nil {
			continue
		}

		var stored struct {
			Type  string `json:"type"`
			Value string `json:"value"`
		}
		if err := json.Unmarshal([]byte(val), &stored); err != nil {
			continue
		}

		entry := &pb.FeatureEntry{
			FeatureKey: featureKey,
			Value:      mapStoredToFeatureValue(stored),
		}
		entries = append(entries, entry)
	}

	// 🔍 Si Redis no tiene nada, consultamos Mongo
	if len(entries) == 0 {
		filter := bson.M{"feature_key": bson.M{"$regex": "^" + prefix}}
		cursor, err := s.mongo.Collection.Find(ctx, filter)
		if err != nil {
			return nil, err
		}
		defer cursor.Close(ctx)

		for cursor.Next(ctx) {
			var doc struct {
				FeatureKey  string            `bson:"feature_key"`
				LastUpdated time.Time         `bson:"last_updated"`
				Value       map[string]string `bson:"value"`
			}
			if err := cursor.Decode(&doc); err != nil {
				continue
			}
			parts := strings.Split(doc.FeatureKey, ":")
			if len(parts) < 4 {
				continue
			}
			featureKey := parts[3]

			stored := struct {
				Type  string `json:"type"`
				Value string `json:"value"`
			}{
				Type:  doc.Value["type"],
				Value: doc.Value["value"],
			}

			entry := &pb.FeatureEntry{
				FeatureKey: featureKey,
				Value:      mapStoredToFeatureValue(stored),
			}
			entries = append(entries, entry)

			payload, _ := json.Marshal(map[string]string{
				"type":  stored.Type,
				"value": stored.Value,
			})
			_ = s.redis.Client.Set(ctx, doc.FeatureKey, payload, 24*time.Hour).Err()
		}
	}

	return &pb.FeatureList{Flags: entries}, nil
}

func main() {
	lis, err := net.Listen("tcp", ":50051")
	if err != nil {
		log.Fatalf("Fallo al escuchar: %v", err)
	}

	// Iniciamos Redis y MongoDB
	redisClient := storage.NewRedisClient()
	mongoClient := storage.NewMongoClient()

	// Creamos el servidor gRPC
	grpcServer := grpc.NewServer()

	// Registramos nuestra implementación
	pb.RegisterFeatureFlagsServer(grpcServer, &featureFlagsServer{
		redis: redisClient,
		mongo: mongoClient,
	})

	reflection.Register(grpcServer)

	log.Println("Servidor gRPC corriendo en puerto 50051...")
	if err := grpcServer.Serve(lis); err != nil {
		log.Fatalf("Fallo al servir: %v", err)
	}
}
