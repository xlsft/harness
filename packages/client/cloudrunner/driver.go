// Package cloudrunner is `harness-client runner`: a runner node of cloud sessions (docs/design/cloud-sessions.md). It
// registers with the server like an always-on host, polls it for the containers it should have, creates and removes them
// through a container engine (Driver), keeps the shared caches under their cap and runs the egress proxy that is the
// containers' only way out. It runs no agent and reads no repository: the daemon in each container does that.
package cloudrunner

import (
	"context"
	"errors"
	"time"
)

// Labels the runner puts on its containers; List finds them by these.
const (
	LabelRunner   = "dev.harness.runner"
	LabelSession  = "dev.harness.cloud-session"
	LabelCacheKey = "dev.harness.cache-key"
)

// ErrNoSuchImage is what Create returns when the image is not on the engine (then Pull, and Create again).
var ErrNoSuchImage = errors.New("no such image")

// ErrNotFound is a container or network the engine doesn't have.
var ErrNotFound = errors.New("not found")

// Mount is a host directory in a container.
type Mount struct {
	Source, Target string
	ReadOnly       bool
}

// File is copied into a created container before it starts.
type File struct {
	// Path is absolute; its directory must exist in the image.
	Path     string
	Mode     int64
	UID, GID int
	Data     []byte
}

// ContainerSpec is what Create makes.
type ContainerSpec struct {
	Name    string
	Image   string
	Labels  map[string]string
	Env     []string
	Cmd     []string
	User    string
	Runtime string
	// Limits; zero leaves the engine's default.
	CPUMillis   int
	MemoryBytes int64
	Pids        int64
	// DiskBytes is passed as the writable layer's size (storage-opt size), which only some storage drivers support.
	DiskBytes int64
	Network   string
	Mounts    []Mount
}

// Container is one the engine has.
type Container struct {
	ID      string
	Name    string
	Labels  map[string]string
	Running bool
	Created time.Time
	// IPs by network name.
	IPs map[string]string
	// SizeRw is the writable layer, when List was asked for sizes; -1 when not.
	SizeRw int64
}

// Network is what EnsureNetwork found or made.
type Network struct {
	ID, Name, Gateway, Subnet string
	Internal                  bool
}

// Driver is what a runner needs of a container engine (decision 17). Docker implements it on the Docker Engine API, which
// Podman serves too; microVMs or Kubernetes would be other implementations.
type Driver interface {
	// Ping checks that the engine answers.
	Ping(ctx context.Context) error
	// EnsureNetwork finds the network by name or makes it; internal: no route out.
	EnsureNetwork(ctx context.Context, name string, internal bool, labels map[string]string) (Network, error)
	// ConnectNetwork attaches a container (the runner's own) to a network under an alias.
	ConnectNetwork(ctx context.Context, network, container string, aliases []string) error
	// Pull fetches an image.
	Pull(ctx context.Context, image string) error
	// Create makes a container without starting it.
	Create(ctx context.Context, spec ContainerSpec) (string, error)
	// PutFiles copies files into a container (created or running).
	PutFiles(ctx context.Context, id string, files []File) error
	Start(ctx context.Context, id string) error
	// Stop asks the container to end and kills it after timeout.
	Stop(ctx context.Context, id string, timeout time.Duration) error
	// Remove removes a container with its writable layer and anonymous volumes, stopped or not.
	Remove(ctx context.Context, id string) error
	// List returns the containers that carry all of these labels, stopped ones too. sizes measures writable layers (slow).
	List(ctx context.Context, labels map[string]string, sizes bool) ([]Container, error)
}
