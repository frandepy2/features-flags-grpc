package storage

import (
	"context"
	"log"
	"time"

	"github.com/redis/go-redis/v9"
)

var ctx = context.Background()

type RedisClient struct {
	Client *redis.Client
}

func NewRedisClient() *RedisClient {
	rdb := redis.NewClient(&redis.Options{
		Addr:     "redis:6379",
		Password: "",
		DB:       0,
	})

	_, err := rdb.Ping(ctx).Result()
	if err != nil {
		log.Fatalf("No se pudo conectar a Redis: %v", err)
	}

	log.Println("Conectado a Redis ✔️")
	return &RedisClient{Client: rdb}
}

func (r *RedisClient) SetFeature(key string, enabled bool) error {
	value := "0"
	if enabled {
		value = "1"
	}
	return r.Client.Set(ctx, key, value, 24*time.Hour).Err()
}

func (r *RedisClient) GetFeature(key string) (bool, error) {
	val, err := r.Client.Get(ctx, key).Result()
	if err == redis.Nil {
		return false, nil
	} else if err != nil {
		return false, err
	}
	return val == "1", nil
}
