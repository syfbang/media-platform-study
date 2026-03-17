package config

import (
	"fmt"
	"os"
	"strings"
)

type Config struct {
	AppPort  string
	Postgres PostgresConfig
	MinIO    MinIOConfig
	Kafka    KafkaConfig
	Redis    RedisConfig
}

type PostgresConfig struct {
	Host, Port, User, Password, DB, SSLMode string
}

func (p PostgresConfig) DSN() string {
	return fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=%s",
		p.Host, p.Port, p.User, p.Password, p.DB, p.SSLMode)
}

type MinIOConfig struct {
	Endpoint, AccessKey, SecretKey, Bucket string
	UseSSL                                 bool
}

type KafkaConfig struct {
	Brokers []string
	Topic   string
}

type RedisConfig struct {
	Addr, Password string
}

func Load() (*Config, error) {
	cfg := &Config{
		AppPort: env("APP_PORT", "4242"),
		Postgres: PostgresConfig{
			Host:     env("POSTGRES_HOST", "localhost"),
			Port:     env("POSTGRES_PORT", "5432"),
			User:     env("POSTGRES_USER", "media"),
			Password: env("POSTGRES_PASSWORD", "media1234"),
			DB:       env("POSTGRES_DB", "media_platform"),
			SSLMode:  env("POSTGRES_SSLMODE", "disable"),
		},
		MinIO: MinIOConfig{
			Endpoint:  env("MINIO_ENDPOINT", "localhost:9000"),
			AccessKey: env("MINIO_ROOT_USER", "minioadmin"),
			SecretKey: env("MINIO_ROOT_PASSWORD", "minioadmin1234"),
			Bucket:    env("MINIO_BUCKET", "media-files"),
			UseSSL:    env("MINIO_USE_SSL", "false") == "true",
		},
		Kafka: KafkaConfig{
			Brokers: strings.Split(env("KAFKA_BROKERS", "localhost:9092"), ","),
			Topic:   env("KAFKA_TOPIC", "media-events"),
		},
		Redis: RedisConfig{
			Addr:     env("REDIS_ADDR", "localhost:6379"),
			Password: env("REDIS_PASSWORD", ""),
		},
	}
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("config validation: %w", err)
	}
	return cfg, nil
}

func (c *Config) validate() error {
	required := map[string]string{
		"APP_PORT":           c.AppPort,
		"POSTGRES_USER":      c.Postgres.User,
		"POSTGRES_PASSWORD":  c.Postgres.Password,
		"POSTGRES_DB":        c.Postgres.DB,
		"MINIO_ENDPOINT":     c.MinIO.Endpoint,
		"MINIO_ACCESS_KEY":   c.MinIO.AccessKey,
		"MINIO_SECRET_KEY":   c.MinIO.SecretKey,
		"MINIO_BUCKET":       c.MinIO.Bucket,
		"KAFKA_BROKERS":      strings.Join(c.Kafka.Brokers, ","),
	}
	for k, v := range required {
		if v == "" {
			return fmt.Errorf("%s is required", k)
		}
	}
	return nil
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
