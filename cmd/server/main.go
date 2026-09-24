package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	server "fracturedexodusserver/server"
	mm "fracturedexodusserver/server/matchmaking"
	playerhandling "fracturedexodusserver/server/playerhandling"
	wstransport "fracturedexodusserver/server/ws"

	"github.com/gorilla/websocket"
)

func main() {
	runServer := flag.Bool("run", false, "run the HTTP server")
	initDB := flag.Bool("init_db", false, "initialize the database")
	resetDB := flag.Bool("reset_db", false, "reset the database")
	initMMDB := flag.Bool("init_mm_db", false, "initialize the matchmaking database")
	resetMMDB := flag.Bool("reset_mm_db", false, "reset the matchmaking database")
	localhost := flag.Bool("localhost", false, "local dev: report 127.0.0.1 as the game server join address (otherwise GAME_SERVER_PUBLIC_HOST is used)")
	flag.Parse()

	if !*runServer && !*initDB && !*resetDB && !*initMMDB && !*resetMMDB {
		flag.Usage()
		return
	}

	ctx := context.Background()
	if *initDB || *resetDB || *initMMDB || *resetMMDB {
		config := server.DefaultDBConfig()
		database, err := server.OpenDB(ctx, config)
		if err != nil {
			log.Fatalf("database connection error: %v", err)
		}
		mmDB, err := server.GetMMDB(ctx)
		if err != nil {
			log.Fatalf("matchmaking database connection error: %v", err)
		}
		defer func() {
			_ = database.Close()
			_ = mmDB.DB.Close()
		}()

		if *resetDB {
			if err := server.ResetDB(ctx, database); err != nil {
				log.Fatalf("reset db error: %v", err)
			}
			log.Printf("database reset complete")
		}

		if *initDB {
			if err := server.InitDB(ctx, database); err != nil {
				log.Fatalf("init db error: %v", err)
			}
			log.Printf("database initialization complete")
		}

		if *initMMDB {
			if err := server.InitMMDB(ctx, mmDB); err != nil {
				log.Fatalf("init matchmaking db error: %v", err)
			}
			log.Printf("matchmaking database initialization complete")
		}

		if *resetMMDB {
			if err := server.ResetMMDB(ctx, mmDB); err != nil {
				log.Fatalf("reset matchmaking db error: %v", err)
			}
			log.Printf("matchmaking database reset complete")
		}
	}

	if *runServer {
		startServer(*localhost)
	}
}

func startServer(localhost bool) {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8000"
	}

	api := server.NewServerAPI("FracturedExodusServer")
	playerAPI := playerhandling.NewPlayerAPI("dev")
	gameServerConfig := server.DefaultGameServerConfig()
	gameServerConfig.Localhost = localhost
	gameServerManager := server.NewGameServerManager(gameServerConfig)
	gameServerAPI := server.NewGameServerAPI(gameServerManager)
	matchmakingAPI := mm.NewMatchmakingAPI("NA", gameServerManager)
	mux := http.NewServeMux()
	api.RegisterRoutes(mux)
	playerAPI.RegisterRoutes(mux)
	gameServerAPI.RegisterRoutes(mux)
	matchmakingAPI.RegisterRoutes(mux)

	// WebSocket transport (/ws), additive alongside the HTTP routes above: same process, same
	// port, same mux. Existing HTTP endpoints are untouched and keep working exactly as today.
	hub := wstransport.NewHub()
	matchmakingAPI.SetHub(hub)
	playerAPI.SetHub(hub)

	wsRouter := wstransport.Router{}
	for msgType, handler := range playerAPI.WSHandlers() {
		wsRouter[msgType] = handler
	}
	for msgType, handler := range matchmakingAPI.WSHandlers() {
		wsRouter[msgType] = handler
	}

	upgrader := websocket.Upgrader{
		ReadBufferSize:  4096,
		WriteBufferSize: 4096,
		// Godot's WebSocketPeer client does not set an Origin header consistent with browser
		// same-origin policy, and this API has no cookie-based session to protect against
		// cross-site use, so origin checking is disabled here rather than misconfigured.
		CheckOrigin: func(r *http.Request) bool { return true },
	}
	wstransport.RegisterHandlers(mux, upgrader, wsRouter)

	httpServer := &http.Server{
		Addr:              ":" + port,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	shutdownErrors := make(chan error, 1)
	go func() {
		stop := make(chan os.Signal, 1)
		signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
		<-stop

		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		if err := gameServerManager.StopAll(shutdownCtx); err != nil {
			log.Printf("failed to stop game servers: %v", err)
		}

		shutdownErrors <- httpServer.Shutdown(shutdownCtx)
	}()

	log.Printf("API listening on :%s", port)
	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("server error: %v", err)
	}

	if err := <-shutdownErrors; err != nil {
		log.Fatalf("shutdown error: %v", err)
	}
}
