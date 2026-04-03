package server

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"
)

type GameServerConfig struct {
	ImageName    string
	DockerFile   string
	BuildContext string
	GamePort     string
	Protocol     string
}

func DefaultGameServerConfig() GameServerConfig {
	return GameServerConfig{
		ImageName:    getEnvOrDefault("GAME_IMAGE", "fractured-exodus-game:dev"),
		DockerFile:   getEnvOrDefault("GAME_DOCKERFILE", "docker/dev.Dockerfile"),
		BuildContext: getEnvOrDefault("GAME_BUILD_CONTEXT", "."),
		GamePort:     getEnvOrDefault("GAME_PORT", "8080"),
		Protocol:     getEnvOrDefault("GAME_PROTOCOL", "udp"),
	}
}

type GameInstance struct {
	ID            string            `json:"id"`
	ContainerID   string            `json:"containerId"`
	ContainerName string            `json:"containerName"`
	Image         string            `json:"image"`
	Host          string            `json:"host"`
	Port          string            `json:"port"`
	Protocol      string            `json:"protocol"`
	JoinKey       string            `json:"joinKey"`
	Ports         map[string]string `json:"ports"`
	Players       []Player          `json:"players"`
	StartedAt     string            `json:"startedAt"`
}

type GameServerManager struct {
	config          GameServerConfig
	buildOnce       sync.Once
	buildErr        error
	mu              sync.Mutex
	registrationKey string
	instances       map[string]GameInstance
}

func NewGameServerManager(config GameServerConfig) *GameServerManager {
	return &GameServerManager{
		config:    config,
		instances: make(map[string]GameInstance),
	}
}

func (manager *GameServerManager) StartGameInstance(ctx context.Context, players []Player, requestedPort string) (GameInstance, error) {
	manager.buildOnce.Do(func() {
		manager.buildErr = manager.buildImage(ctx)
	})
	if manager.buildErr != nil {
		return GameInstance{}, manager.buildErr
	}

	containerName := fmt.Sprintf("game-instance-%d", time.Now().UnixNano())
	containerID, err := manager.runContainer(ctx, containerName, requestedPort)
	if err != nil {
		return GameInstance{}, err
	}

	ports, err := manager.inspectPorts(ctx, containerName)
	if err != nil {
		return GameInstance{}, err
	}

	containerPort := fmt.Sprintf("%s/%s", manager.config.GamePort, manager.config.Protocol)
	hostPort := ports[containerPort]
	if hostPort == "" {
		hostPort = requestedPort
	}

	joinKey, err := generateJoinKey()
	if err != nil {
		return GameInstance{}, err
	}

	instance := GameInstance{
		ID:            fmt.Sprintf("instance-%s", containerName),
		ContainerID:   containerID,
		ContainerName: containerName,
		Image:         manager.config.ImageName,
		Host:          "127.0.0.1",
		Port:          hostPort,
		Protocol:      manager.config.Protocol,
		JoinKey:       joinKey,
		Ports:         ports,
		Players:       players,
		StartedAt:     time.Now().UTC().Format(time.RFC3339),
	}

	manager.mu.Lock()
	manager.instances[instance.ID] = instance
	manager.mu.Unlock()

	manager.streamContainerLogs(containerName)

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

func (manager *GameServerManager) runContainer(ctx context.Context, containerName string, requestedPort string) (string, error) {
	containerPort := fmt.Sprintf("%s/%s", manager.config.GamePort, manager.config.Protocol)
	registrationKey, err := manager.getOrCreateRegistrationKey()
	if err != nil {
		return "", err
	}

	envArgs := []string{
		"-e", fmt.Sprintf("MM_SERVER_NAME=%s", containerName),
		"-e", fmt.Sprintf("MM_SERVER_REGISTRATION_KEY=%s", registrationKey),
		"-e", "HOST_GATEWAY_IP=host.docker.internal",
	}

	var cmd *exec.Cmd
	if requestedPort != "" {
		portMapping := fmt.Sprintf("%s:%s", requestedPort, containerPort)
		args := []string{"run", "-d", "--rm", "-p", portMapping, "--name", containerName}
		args = append(args, envArgs...)
		args = append(args, manager.config.ImageName)
		cmd = exec.CommandContext(ctx, "docker", args...)
	} else {
		args := []string{"run", "-d", "--rm", "-P", "--name", containerName}
		args = append(args, envArgs...)
		args = append(args, manager.config.ImageName)
		cmd = exec.CommandContext(ctx, "docker", args...)
	}
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}

	return strings.TrimSpace(string(output)), nil
}

func (manager *GameServerManager) getOrCreateRegistrationKey() (string, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()

	if manager.registrationKey != "" {
		return manager.registrationKey, nil
	}

	registrationKey, err := generateJoinKey()
	if err != nil {
		return "", fmt.Errorf("generate registration key: %w", err)
	}
	if err := os.Setenv("MM_SERVER_REGISTRATION_KEY", registrationKey); err != nil {
		return "", fmt.Errorf("set registration key env: %w", err)
	}

	manager.registrationKey = registrationKey
	return manager.registrationKey, nil
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

func (manager *GameServerManager) ListInstances() []GameInstance {
	manager.mu.Lock()
	defer manager.mu.Unlock()

	instances := make([]GameInstance, 0, len(manager.instances))
	for _, instance := range manager.instances {
		instances = append(instances, instance)
	}

	return instances
}

func (manager *GameServerManager) StopAll(ctx context.Context) error {
	manager.mu.Lock()
	instances := make([]GameInstance, 0, len(manager.instances))
	for _, instance := range manager.instances {
		instances = append(instances, instance)
	}
	manager.mu.Unlock()

	containerNameSet := make(map[string]struct{}, len(instances))
	for _, instance := range instances {
		if instance.ContainerName != "" {
			containerNameSet[instance.ContainerName] = struct{}{}
		}
	}

	discoveredNames, err := manager.listManagedContainerNames(ctx)
	if err != nil {
		return err
	}
	for _, name := range discoveredNames {
		containerNameSet[name] = struct{}{}
	}

	containerNames := make([]string, 0, len(containerNameSet))
	for name := range containerNameSet {
		containerNames = append(containerNames, name)
	}
	slices.Sort(containerNames)

	var stopErrors []error
	for _, name := range containerNames {
		cmd := exec.CommandContext(ctx, "docker", "stop", name)
		output, stopErr := cmd.CombinedOutput()
		if stopErr != nil {
			if strings.Contains(strings.ToLower(string(output)), "no such container") {
				continue
			}
			stopErrors = append(stopErrors, fmt.Errorf("stop %s: %w: %s", name, stopErr, strings.TrimSpace(string(output))))
		}
	}

	manager.mu.Lock()
	manager.instances = make(map[string]GameInstance)
	manager.mu.Unlock()

	if len(stopErrors) > 0 {
		return errors.Join(stopErrors...)
	}

	return nil
}

func (manager *GameServerManager) listManagedContainerNames(ctx context.Context) ([]string, error) {
	cmd := exec.CommandContext(ctx, "docker", "ps", "--filter", "name=game-instance-", "--format", "{{.Names}}")
	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	names := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		names = append(names, trimmed)
	}

	return names, nil
}

func (manager *GameServerManager) streamContainerLogs(containerName string) {
	go func() {
		cmd := exec.Command("docker", "logs", "-f", containerName)

		pipe, err := cmd.StdoutPipe()
		if err != nil {
			fmt.Printf("[container:%s][logs] failed to create stdout pipe: %v\n", containerName, err)
			return
		}
		cmd.Stderr = cmd.Stdout

		if err := cmd.Start(); err != nil {
			fmt.Printf("[container:%s][logs] failed to start docker logs: %v\n", containerName, err)
			return
		}

		pipeContainerLogStream(containerName, pipe)

		if waitErr := cmd.Wait(); waitErr != nil {
			fmt.Printf("[container:%s][logs] docker logs exited: %v\n", containerName, waitErr)
		}
	}()
}

func pipeContainerLogStream(containerName string, reader io.Reader) {
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		fmt.Printf("[container:%s] %s\n", containerName, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		fmt.Printf("[container:%s] scanner error: %v\n", containerName, err)
	}
}

func generateJoinKey() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
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
		Port    string   `json:"port"`
	}
	if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
		response.WriteHeader(http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(request.Context(), 2*time.Minute)
	defer cancel()

	instance, err := api.manager.StartGameInstance(ctx, payload.Players, payload.Port)
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
