package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

type GameServerConfig struct {
	ImageName    string
	DockerFile   string
	BuildContext string
}

func DefaultGameServerConfig() GameServerConfig {
	return GameServerConfig{
		ImageName:    getEnvOrDefault("GAME_IMAGE", "fractured-exodus-game:dev"),
		DockerFile:   getEnvOrDefault("GAME_DOCKERFILE", "docker/dev.Dockerfile"),
		BuildContext: getEnvOrDefault("GAME_BUILD_CONTEXT", "."),
	}
}

type GameInstance struct {
	ID            string            `json:"id"`
	ContainerID   string            `json:"containerId"`
	ContainerName string            `json:"containerName"`
	Image         string            `json:"image"`
	Host          string            `json:"host"`
	Ports         map[string]string `json:"ports"`
	Players       []Player          `json:"players"`
	StartedAt     string            `json:"startedAt"`
}

type GameServerManager struct {
	config    GameServerConfig
	buildOnce sync.Once
	buildErr  error
	mu        sync.Mutex
	instances map[string]GameInstance
}

func NewGameServerManager(config GameServerConfig) *GameServerManager {
	return &GameServerManager{
		config:    config,
		instances: make(map[string]GameInstance),
	}
}

func (manager *GameServerManager) StartGameInstance(ctx context.Context, players []Player) (GameInstance, error) {
	manager.buildOnce.Do(func() {
		manager.buildErr = manager.buildImage(ctx)
	})
	if manager.buildErr != nil {
		return GameInstance{}, manager.buildErr
	}

	containerName := fmt.Sprintf("game-instance-%d", time.Now().UnixNano())
	containerID, err := manager.runContainer(ctx, containerName)
	if err != nil {
		return GameInstance{}, err
	}

	ports, err := manager.inspectPorts(ctx, containerName)
	if err != nil {
		return GameInstance{}, err
	}

	instance := GameInstance{
		ID:            fmt.Sprintf("instance-%s", containerName),
		ContainerID:   containerID,
		ContainerName: containerName,
		Image:         manager.config.ImageName,
		Host:          "127.0.0.1",
		Ports:         ports,
		Players:       players,
		StartedAt:     time.Now().UTC().Format(time.RFC3339),
	}

	manager.mu.Lock()
	manager.instances[instance.ID] = instance
	manager.mu.Unlock()

	return instance, nil
}

func (manager *GameServerManager) buildImage(ctx context.Context) error {
	if manager.config.DockerFile == "" || manager.config.BuildContext == "" {
		return errors.New("docker build config is missing")
	}

	cmd := exec.CommandContext(ctx, "docker", "build", "-f", manager.config.DockerFile, "-t", manager.config.ImageName, manager.config.BuildContext)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func (manager *GameServerManager) runContainer(ctx context.Context, containerName string) (string, error) {
	cmd := exec.CommandContext(ctx, "docker", "run", "-d", "-P", "--name", containerName, manager.config.ImageName)
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}

	return strings.TrimSpace(string(output)), nil
}

func (manager *GameServerManager) inspectPorts(ctx context.Context, containerName string) (map[string]string, error) {
	cmd := exec.CommandContext(ctx, "docker", "port", containerName)
	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	ports := make(map[string]string)
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	for _, line := range lines {
		if line == "" {
			continue
		}
		parts := strings.Split(line, " -> ")
		if len(parts) != 2 {
			continue
		}
		containerPort := strings.TrimSpace(parts[0])
		hostPort := strings.TrimSpace(parts[1])
		ports[containerPort] = hostPort
	}

	return ports, nil
}

type GameServerAPI struct {
	manager *GameServerManager
}

func NewGameServerAPI(manager *GameServerManager) *GameServerAPI {
	return &GameServerAPI{manager: manager}
}

func (api *GameServerAPI) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/game/server/start", api.handleStart)
}

func (api *GameServerAPI) handleStart(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		response.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	var payload struct {
		Players []Player `json:"players"`
	}
	if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
		response.WriteHeader(http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(request.Context(), 2*time.Minute)
	defer cancel()

	instance, err := api.manager.StartGameInstance(ctx, payload.Players)
	if err != nil {
		response.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(response).Encode(map[string]string{
			"error": err.Error(),
		})
		return
	}

	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(response).Encode(instance)
}
