package storage

import (
	"context"
	"fmt"
	"log"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

type MongoClient struct {
	Client     *mongo.Client
	Collection *mongo.Collection
}

func NewMongoClient() *MongoClient {
	clientOptions := options.Client().ApplyURI("mongodb://mongodb:27017")
	client, err := mongo.Connect(context.Background(), clientOptions)
	if err != nil {
		log.Fatalf("Error al conectar a MongoDB: %v", err)
	}

	// Seleccionamos la base de datos y colección
	collection := client.Database("feature_flags_db").Collection("flags")

	return &MongoClient{
		Client:     client,
		Collection: collection,
	}
}

// Guardar feature flag en MongoDB
func (m *MongoClient) SetFeature(ctx context.Context, key string, value map[string]string) error {
	_, err := m.Collection.UpdateOne(
		ctx,
		bson.M{"feature_key": key},
		bson.M{
			"$set": bson.M{"value": value, "last_updated": time.Now()},
		},
		options.Update().SetUpsert(true),
	)
	return err
}

// Obtener feature flag desde MongoDB
func (m *MongoClient) GetFeature(ctx context.Context, key string) (map[string]string, error) {
	var result bson.M
	err := m.Collection.FindOne(ctx, bson.M{"feature_key": key}).Decode(&result)
	if err != nil {
		return nil, err
	}
	// Convertimos bson.M a map[string]string
	convertedResult := make(map[string]string)
	for key, value := range result {
		// Convierte todos los valores a string (esto es muy dependiente de cómo guardes los datos en Mongo)
		convertedResult[key] = fmt.Sprintf("%v", value)
	}

	return convertedResult, nil
}
