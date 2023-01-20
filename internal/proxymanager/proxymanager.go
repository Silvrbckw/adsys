// Package proxymanager implements an API for configuring proxy-related settings
// on the client machine.
package proxymanager

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/ubuntu/adsys/internal/decorate"
	log "github.com/ubuntu/adsys/internal/grpc/logstreamer"
	"github.com/ubuntu/adsys/internal/i18n"
)

type options struct {
	aptConfigPath         string
	environmentConfigPath string
}
type option func(*options)

// Manager prevents multiple writes to the configuration files in parallel.
type Manager struct {
	proxies []proxySetting

	aptConfigPath         string
	environmentConfigPath string

	applyMu sync.Mutex
}

// New returns a new instance of a proxy manager.
func New(ctx context.Context, config Config, opts ...option) (m *Manager, err error) {
	defer decorate.OnError(&err, i18n.G("couldn't create proxy manager"))

	// defaults
	args := options{
		environmentConfigPath: "/etc/environment.d/99adsys-proxy.conf",
		aptConfigPath:         "/etc/apt/apt.conf.d/99adsys-proxy",
	}
	// applied options
	for _, o := range opts {
		o(&args)
	}

	proxies, err := setConfig(config)
	if err != nil {
		return nil, err
	}

	return &Manager{
		proxies:               proxies,
		aptConfigPath:         args.aptConfigPath,
		environmentConfigPath: args.environmentConfigPath,
	}, nil
}

// Apply applies the proxy configuration to the system.
func (m *Manager) Apply(ctx context.Context) error {
	m.applyMu.Lock()
	defer m.applyMu.Unlock()

	if err := m.applyEnvironmentProxy(ctx); err != nil {
		return err
	}

	return nil
}

func (m *Manager) applyEnvironmentProxy(ctx context.Context) (err error) {
	defer decorate.OnError(&err, i18n.G("couldn't apply environment proxy configuration"))

	content := "### This file was generated by ADSys - manual changes will be overwritten\n"
	for _, p := range m.proxies {
		content += p.envString()
	}

	if exists, prevContent, err := prevConfIfExists(m.environmentConfigPath); exists && prevContent == content {
		log.Debugf(ctx, fmt.Sprintf("Environment proxy configuration at %q is already up to date", m.environmentConfigPath))
		return nil
	} else if err != nil {
		return err
	}

	// Check if the parent directory exists - attempt to create the structure if not
	environmentConfigDir := filepath.Dir(m.environmentConfigPath)
	if _, err := os.Stat(filepath.Dir(m.environmentConfigPath)); errors.Is(err, os.ErrNotExist) {
		log.Debugf(ctx, fmt.Sprintf("Creating directory %q", environmentConfigDir))
		// #nosec G301 - /etc/environment.d permissions are 0755, so we should keep the same pattern
		if err := os.MkdirAll(environmentConfigDir, 0755); err != nil {
			return fmt.Errorf("failed to create environment config parent directory: %w", err)
		}
	}

	// #nosec G306 - /etc/environment.d/* permissions are 0644, so we should keep the same pattern
	if err := os.WriteFile(m.environmentConfigPath, []byte(content), 0644); err != nil {
		return err
	}

	return nil
}

// prevConfIfExists returns the previous configuration if it exists. No error is
// returned if the file doesn't exist, but other errors are.
func prevConfIfExists(path string) (exists bool, content string, err error) {
	defer decorate.OnError(&err, i18n.G("couldn't read previous configuration"))

	if prevConf, err := os.ReadFile(path); err == nil {
		return true, string(prevConf), nil
	} else if !os.IsNotExist(err) {
		return false, "", err
	}

	return false, "", nil
}
