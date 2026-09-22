package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

var version = "dev"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "bootstrap":
		err = commandBootstrap(os.Args[2:])
	case "serve", "server":
		err = commandServe(os.Args[2:])
	case "project":
		err = commandProject(os.Args[2:])
	case "issue":
		err = commandIssue(os.Args[2:])
	case "key":
		err = commandKey(os.Args[2:])
	case "version":
		printJSON(map[string]string{"version": version, "api": "v1"})
	default:
		usage()
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `simpletracker — a small self-hosted issue tracker

Commands:
  bootstrap --data-dir DIR [--name NAME]       create the first API key
  serve --data-dir DIR [--listen ADDR]         run the HTTP server
	project create|list|get                       project operations
	issue create|list|get|update|close|reopen    issue operations
  issue comment|label|assign|unassign|parent|block|unblock|order|frontier
  key create|list|revoke                        API key operations

All project/issue/key client commands emit JSON and accept --server and --api-key.`)
}

func commandBootstrap(args []string) error {
	flags := flag.NewFlagSet("bootstrap", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	dataDir := flags.String("data-dir", "./data", "persistent data directory")
	name := flags.String("name", "bootstrap", "human-readable key name")
	if err := flags.Parse(args); err != nil {
		return err
	}
	store, err := OpenStore(*dataDir)
	if err != nil {
		return err
	}
	defer store.Close()
	key, err := store.BootstrapKey(context.Background(), *name)
	if errors.Is(err, ErrAlreadyExists) {
		return fmt.Errorf("already initialized: an active API key exists")
	}
	if err != nil {
		return err
	}
	printJSON(key)
	return nil
}

func commandServe(args []string) error {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	dataDir := flags.String("data-dir", "./data", "persistent data directory")
	listen := flags.String("listen", "127.0.0.1:8080", "HTTP listen address")
	baseURL := flags.String("base-url", "", "canonical public URL")
	bootstrapName := flags.String("bootstrap-key", "", "create a first key when no active key exists")
	if err := flags.Parse(args); err != nil {
		return err
	}
	store, err := OpenStore(*dataDir)
	if err != nil {
		return err
	}
	defer store.Close()
	count, err := store.KeyCount(context.Background())
	if err != nil {
		return err
	}
	if count == 0 && strings.TrimSpace(*bootstrapName) != "" {
		key, bootstrapErr := store.BootstrapKey(context.Background(), *bootstrapName)
		if bootstrapErr != nil {
			return bootstrapErr
		}
		fmt.Fprintf(os.Stderr, "bootstrap API key (shown once): %s\n", key.Secret)
	} else if count == 0 {
		return fmt.Errorf("no API keys configured; run `simpletracker bootstrap --data-dir %s`", *dataDir)
	}
	server := NewServer(store, *baseURL, version)
	httpServer := &http.Server{Addr: *listen, Handler: server.Handler(), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
	}()
	fmt.Fprintf(os.Stderr, "simpletracker listening on %s\n", *listen)
	err = httpServer.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

type cliOptions struct {
	server  string
	key     string
	actor   string
	session string
}

func addClientFlags(flags *flag.FlagSet) *cliOptions {
	options := &cliOptions{}
	flags.StringVar(&options.server, "server", "http://127.0.0.1:8080", "tracker server URL")
	flags.StringVar(&options.key, "api-key", os.Getenv("SIMPLETRACKER_API_KEY"), "API key (or SIMPLETRACKER_API_KEY)")
	flags.StringVar(&options.actor, "actor", "", "actor attribution")
	flags.StringVar(&options.session, "session", "", "session attribution")
	return options
}

func commandProject(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("project action is required")
	}
	action := args[0]
	flags := flag.NewFlagSet("project "+action, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	opts := addClientFlags(flags)
	name := flags.String("name", "", "project name")
	slug := flags.String("slug", "", "project slug")
	id := flags.String("id", "", "project id or slug")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	switch action {
	case "create":
		return opts.request(http.MethodPost, "/api/v1/projects", map[string]string{"name": *name, "slug": *slug})
	case "list":
		return opts.request(http.MethodGet, "/api/v1/projects", nil)
	case "get":
		if *id == "" {
			return fmt.Errorf("--id is required")
		}
		return opts.request(http.MethodGet, "/api/v1/projects/"+urlPath(*id), nil)
	default:
		return fmt.Errorf("unknown project action %q", action)
	}
}

func commandIssue(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("issue action is required")
	}
	action := args[0]
	flags := flag.NewFlagSet("issue "+action, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	opts := addClientFlags(flags)
	project := flags.String("project", "", "project id or slug")
	ref := flags.String("id", "", "issue id or project#number")
	title := flags.String("title", "", "issue title")
	body := flags.String("body", "", "Markdown body")
	parent := flags.String("parent", "", "parent issue id or project#number")
	label := flags.String("label", "", "issue label")
	labels := flags.String("labels", "", "comma-separated issue labels")
	assignee := flags.String("assignee", "", "assignee")
	state := flags.String("state", "", "open or closed")
	assigned := flags.String("assigned", "", "assigned or unassigned")
	search := flags.String("q", "", "text search")
	age := flags.String("age", "", "maximum age since update (duration or days)")
	updatedSince := flags.String("updated-since", "", "updated since RFC3339 timestamp")
	updatedUntil := flags.String("updated-until", "", "updated until RFC3339 timestamp")
	position := flags.Int64("position", 0, "sibling position")
	remove := flags.Bool("remove", false, "remove a label")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	switch action {
	case "create":
		if *project == "" {
			return fmt.Errorf("--project is required")
		}
		payload := map[string]any{"title": *title, "body": *body}
		if *parent != "" {
			payload["parent_id"] = *parent
		}
		if *assignee != "" {
			payload["assignee"] = *assignee
		}
		if *labels != "" {
			payload["labels"] = splitCSV(*labels)
		}
		return opts.request(http.MethodPost, "/api/v1/projects/"+urlPath(*project)+"/issues", payload)
	case "list":
		path := "/api/v1/issues"
		if *project != "" {
			path = "/api/v1/projects/" + urlPath(*project) + "/issues"
		}
		return opts.request(http.MethodGet, addIssueQuery(path, *parent, *label, *assignee, *state, *assigned, *search, *age, *updatedSince, *updatedUntil), nil)
	case "frontier":
		path := "/api/v1/frontier"
		if *project != "" {
			path = "/api/v1/projects/" + urlPath(*project) + "/frontier"
		}
		return opts.request(http.MethodGet, addIssueQuery(path, *parent, "", "", "", "unassigned", "", "", "", ""), nil)
	case "get":
		if *ref == "" {
			return fmt.Errorf("--id is required")
		}
		return opts.request(http.MethodGet, "/api/v1/issues/"+urlPath(*ref), nil)
	case "update":
		if *ref == "" {
			return fmt.Errorf("--id is required")
		}
		payload := make(map[string]string)
		for _, name := range []string{"title", "body"} {
			wasSet := false
			flags.Visit(func(flag *flag.Flag) {
				if flag.Name == name {
					wasSet = true
				}
			})
			if wasSet {
				if name == "title" {
					payload[name] = *title
				} else {
					payload[name] = *body
				}
			}
		}
		if len(payload) == 0 {
			return fmt.Errorf("at least one of --title or --body is required")
		}
		return opts.request(http.MethodPatch, "/api/v1/issues/"+urlPath(*ref), payload)
	case "close", "reopen":
		if *ref == "" {
			return fmt.Errorf("--id is required")
		}
		return opts.request(http.MethodPost, "/api/v1/issues/"+urlPath(*ref)+"/"+action, nil)
	case "comment":
		if *ref == "" || *body == "" {
			return fmt.Errorf("--id and --body are required")
		}
		return opts.request(http.MethodPost, "/api/v1/issues/"+urlPath(*ref)+"/comments", map[string]string{"body": *body})
	case "label":
		if *ref == "" || *label == "" {
			return fmt.Errorf("--id and --label are required")
		}
		if *remove {
			return opts.request(http.MethodDelete, "/api/v1/issues/"+urlPath(*ref)+"/labels/"+urlPath(*label), nil)
		}
		return opts.request(http.MethodPost, "/api/v1/issues/"+urlPath(*ref)+"/labels", map[string]string{"label": *label})
	case "assign":
		if *ref == "" || *assignee == "" {
			return fmt.Errorf("--id and --assignee are required")
		}
		return opts.request(http.MethodPost, "/api/v1/issues/"+urlPath(*ref)+"/assign", map[string]string{"assignee": *assignee})
	case "unassign":
		if *ref == "" {
			return fmt.Errorf("--id is required")
		}
		return opts.request(http.MethodDelete, "/api/v1/issues/"+urlPath(*ref)+"/assign", nil)
	case "parent":
		if *ref == "" {
			return fmt.Errorf("--id is required")
		}
		if *parent == "" {
			return opts.request(http.MethodDelete, "/api/v1/issues/"+urlPath(*ref)+"/parent", nil)
		}
		return opts.request(http.MethodPost, "/api/v1/issues/"+urlPath(*ref)+"/parent", map[string]string{"parent_id": *parent})
	case "block", "unblock":
		if *ref == "" || *parent == "" {
			return fmt.Errorf("--id and --parent (blocker issue) are required")
		}
		method := http.MethodPost
		if action == "unblock" {
			method = http.MethodDelete
		}
		path := "/api/v1/issues/" + urlPath(*ref) + "/blockers/" + urlPath(*parent)
		return opts.request(method, path, nil)
	case "order":
		if *ref == "" {
			return fmt.Errorf("--id is required")
		}
		return opts.request(http.MethodPost, "/api/v1/issues/"+urlPath(*ref)+"/position", map[string]int64{"position": *position})
	default:
		return fmt.Errorf("unknown issue action %q", action)
	}
}

func splitCSV(value string) []string {
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

func addIssueQuery(path, parent, label, assignee, state, assigned, search, age, updatedSince, updatedUntil string) string {
	query := url.Values{}
	for key, value := range map[string]string{"parent": parent, "label": label, "assignee": assignee, "state": state, "assigned": assigned, "q": search, "age": age, "updated_since": updatedSince, "updated_until": updatedUntil} {
		if value != "" {
			query.Set(key, value)
		}
	}
	if encoded := query.Encode(); encoded != "" {
		return path + "?" + encoded
	}
	return path
}

func commandKey(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("key action is required")
	}
	action := args[0]
	flags := flag.NewFlagSet("key "+action, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	opts := addClientFlags(flags)
	name := flags.String("name", "", "key name")
	id := flags.String("id", "", "key id")
	includeRevoked := flags.Bool("include-revoked", false, "include revoked keys")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	switch action {
	case "create":
		return opts.request(http.MethodPost, "/api/v1/keys", map[string]string{"name": *name})
	case "list":
		path := "/api/v1/keys"
		if *includeRevoked {
			path += "?include_revoked=true"
		}
		return opts.request(http.MethodGet, path, nil)
	case "revoke":
		if *id == "" {
			return fmt.Errorf("--id is required")
		}
		return opts.request(http.MethodPost, "/api/v1/keys/"+urlPath(*id)+"/revoke", nil)
	default:
		return fmt.Errorf("unknown key action %q", action)
	}
}

func (o *cliOptions) request(method, path string, payload any) error {
	var body io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	request, err := http.NewRequest(method, strings.TrimRight(o.server, "/")+path, body)
	if err != nil {
		return err
	}
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if o.key != "" {
		request.Header.Set("Authorization", "Bearer "+o.key)
	}
	if o.actor != "" {
		request.Header.Set("X-Actor-ID", o.actor)
	}
	if o.session != "" {
		request.Header.Set("X-Session-ID", o.session)
	}
	client := &http.Client{Timeout: 15 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		return err
	}
	var value any
	if err := json.Unmarshal(data, &value); err == nil {
		printJSON(value)
	} else {
		fmt.Print(string(data))
	}
	if response.StatusCode >= 400 {
		return fmt.Errorf("server returned %s", response.Status)
	}
	return nil
}

func urlPath(value string) string {
	value = strings.ReplaceAll(value, "%", "%25")
	value = strings.ReplaceAll(value, "/", "%2F")
	value = strings.ReplaceAll(value, "#", "%23")
	return value
}

func printJSON(value any) {
	data, _ := json.MarshalIndent(value, "", "  ")
	fmt.Println(string(data))
}
