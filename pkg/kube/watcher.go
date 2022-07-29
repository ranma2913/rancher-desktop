// Package kube watches Kubernetes for NodePort services and forces a listener
// on 127.0.0.1, so that it can be picked up by various automatic port
// forwarding mechanisms.
package kube

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"time"

	"github.com/Masterminds/log-go"
	"github.com/rancher-sandbox/rancher-desktop-agent/pkg/tcplistener"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	restclient "k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// watcherState is an enumeration to track the state of the watcher.
type watcherState int

const (
	// stateNoConfig is before the configuration has been loaded
	stateNoConfig watcherState = iota
	// stateDisconnected is when the configuration has been loaded, but not connected.
	stateDisconnected
	stateConnected
	stateWatching
)

// WatchForNodePortServices watches Kubernetes for NodePort services and create
// listeners on 127.0.0.1 matching them.
//
// Any connection errors are ignored and retried.
//
// XXX bug(mook): on irrelevant change, this closes & reopens the port.
func WatchForNodePortServices(ctx context.Context, tracker *tcplistener.ListenerTracker, configPath string) error {
	// These variables are shared across the different states
	state := stateNoConfig
	var err error
	var config *restclient.Config
	var clientset *kubernetes.Clientset
	var eventCh <-chan event
	var errorCh <-chan error
	watchContext, watchCancel := context.WithCancel(ctx)
	localhost := net.IPv4(127, 0, 0, 1)

	// Always cancel if we failed; however, we may clobber watchCancel, so we
	// need a wrapper function to capture the variable reference.
	defer func() {
		watchCancel()
	}()

	for {
		switch state {
		case stateNoConfig:
			config, err = getClientConfig(configPath)
			if err != nil {
				log.Debugw("kubernetes: failed to read kubeconfig", log.Fields{
					"config-path": configPath,
					"error":       err,
				})
				if errors.Is(err, fs.ErrNotExist) {
					// Wait for the file to exist
					time.Sleep(time.Second)
					continue
				}
				return err
			}
			log.Debugf("kubernetes: loaded kubeconfig %s", configPath)
			state = stateDisconnected
		case stateDisconnected:
			clientset, err = kubernetes.NewForConfig(config)
			if err != nil {
				// There should be no transient errors here
				log.Errorw("failed to load kubeconfig", log.Fields{
					"config-path": configPath,
					"error":       err,
				})
				return fmt.Errorf("failed to create Kubernetes client: %w", err)
			}
			eventCh, errorCh, err = watchServices(watchContext, clientset)
			if err != nil {
				if isTimeout(err) {
					// If it's a time out, the server may not be running yet
					time.Sleep(time.Second)
					continue
				}
				return err
			}
			log.Debugf("watching kubernetes services")
			state = stateWatching
		case stateWatching:
			select {
			case err = <-errorCh:
				log.Debugw("kubernetes: got error, rolling back", log.Fields{
					"error": err,
				})
				clientset = nil
				watchCancel()
				watchContext, watchCancel = context.WithCancel(ctx)
				state = stateNoConfig
				time.Sleep(time.Second)
				continue
			case event := <-eventCh:
				if event.service.Spec.Type != corev1.ServiceTypeNodePort {
					// Ignore any non-NodePort errors
					log.Debugf("kubernetes service: not node port %s/%s", event.service.Namespace, event.service.Name)
					continue
				}
				if event.deleted {
					for _, port := range event.service.Spec.Ports {
						if err := tracker.Remove(localhost, int(port.NodePort)); err != nil {
							log.Errorw("failed to close listener", log.Fields{
								"error":     err,
								"port":      port.NodePort,
								"namespace": event.service.Namespace,
								"name":      event.service.Name,
							})
							continue
						}
						log.Debugw("kuberentes service: deleted listener", log.Fields{
							"namespace": event.service.Namespace,
							"name":      event.service.Name,
							"port":      port.NodePort,
						})
					}
				} else {
					for _, port := range event.service.Spec.Ports {
						if err := tracker.Add(localhost, int(port.NodePort)); err != nil {
							log.Errorw("failed to create listener", log.Fields{
								"error":     err,
								"port":      port.NodePort,
								"namespace": event.service.Namespace,
								"name":      event.service.Name,
							})
							continue
						}
						log.Debugw("kubernetes service: started listener", log.Fields{
							"namespace": event.service.Namespace,
							"name":      event.service.Name,
							"port":      port.NodePort,
						})
					}
				}
			}
		}
	}
}

// getClientConfig returns a rest config.
func getClientConfig(configPath string) (*restclient.Config, error) {
	loadingRules := clientcmd.ClientConfigLoadingRules{
		ExplicitPath: configPath,
	}
	clientConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(&loadingRules, nil)
	config, err := clientConfig.ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("could not load Kubernetes client config from %s: %w", configPath, err)
	}
	return config, nil
}

func isTimeout(err error) bool {
	var timeoutError interface{
		Timeout() bool
	}
	if !errors.As(err, &timeoutError) {
		return timeoutError.Timeout()
	}
	return false
}