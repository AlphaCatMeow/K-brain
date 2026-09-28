package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"syscall"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/backend"
	"github.com/Stack-Cairn/K-brain/internal/config"
	sysprompt "github.com/Stack-Cairn/K-brain/internal/prompts"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/routing"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

func backendCLI(args []string) error {
	fs := flag.NewFlagSet("backend", flag.ContinueOnError)
	listen := fs.String("listen", "127.0.0.1:47321", "HTTP listen address")
	token := fs.String("token", os.Getenv("K_BRAIN_BACKEND_TOKEN"), "Bearer token; defaults to K_BRAIN_BACKEND_TOKEN")
	configPath := fs.String("config", "", "Configuration file; defaults to the user configuration")
	sessionDir := fs.String("session-dir", "", "Session storage directory; defaults to project storage")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: kn backend [-listen address] [-token bearer-token]")
		fmt.Fprintln(os.Stderr, "serve the K-brain canonical session API for LiveAgent")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	var cfg *config.Config
	var err error
	if *configPath != "" {
		cfg, err = config.LoadFile(*configPath)
	} else {
		cfg, err = config.Load()
	}
	if err != nil {
		return err
	}
	var store *session.Store
	if *sessionDir != "" {
		store, err = session.Open(*sessionDir)
	} else {
		var dir string
		dir, err = config.Dir()
		if err == nil {
			store, err = session.OpenProjectHome(dir)
		}
	}
	if err != nil {
		return fmt.Errorf("session storage: %w", err)
	}
	defer store.Close()

	configFilename := *configPath
	if configFilename == "" {
		configFilename, err = config.Path()
		if err != nil {
			return err
		}
	}
	settings := backend.NewSettingsStore(cfg, func(next *config.Config) error {
		return next.SaveFile(configFilename)
	})

	models := make([]backendModel, 0, len(cfg.Models))
	for name, model := range cfg.Models {
		provider := routing.ResolvedProvider(cfg, name, "")
		models = append(models, backendModel{name: name, provider: provider, id: model.ID})
	}
	sort.Slice(models, func(i, j int) bool { return models[i].name < models[j].name })
	modelRefs := make([]protocol.ModelRef, 0, len(models))
	for _, model := range models {
		id := model.id
		if id == "" {
			id = model.name
		}
		modelRefs = append(modelRefs, protocol.ModelRef{Provider: model.provider, Model: id})
	}

	factory := func(ctx context.Context, cwd string, selected protocol.ModelRef) (*agent.Agent, error) {
		current := settings.Snapshot()
		modelName := selected.Model
		provider := selected.Provider
		if modelName == "" {
			modelName = current.DefaultModel
		}
		route, err := routing.ResolveRouteContext(ctx, current, modelName, provider, false)
		if err != nil {
			return nil, err
		}
		ag := agent.New(route.Client, route.APIModel, route.MaxOutput, sysprompt.Build(cwd, time.Now())+sysprompt.SkillsPrompt(cwd), agent.WithExperimental(current.Experimental))
		ag.ModelName, ag.Provider = route.ModelName, route.ProviderName
		ag.ContextLimit, ag.Vision, ag.WorkingDir = route.ContextLimit, route.Vision, cwd
		ag.WorktreeSubagents = current.WorktreeSubagents != nil && *current.WorktreeSubagents
		ag.SandboxPolicy = current.Sandbox.Policy(cwd)
		if current.TaskModel != "" {
			if task, taskErr := routing.TaskDefaultForContext(ctx, current); taskErr == nil {
				ag.TaskDefault = task
			}
		}
		return ag, nil
	}

	server, err := backend.New(backend.Options{
		Store:      store,
		Factory:    factory,
		EventDir:   store.SessionsDir() + string(os.PathSeparator) + "backend-events",
		Token:      *token,
		Models:     modelRefs,
		Settings:   settings,
		DefaultCWD: cwd(),
	})
	if err != nil {
		return err
	}
	defer server.Close()

	httpServer := &http.Server{Addr: *listen, Handler: server, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 0, IdleTimeout: 2 * time.Minute}
	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- httpServer.Serve(listener) }()
	fmt.Printf("k-brain backend listening on http://%s\n", listener.Addr())
	sigCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	select {
	case <-sigCtx.Done():
	case err := <-serveErr:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return httpServer.Shutdown(shutdownCtx)
}

type backendModel struct {
	name, provider, id string
}
