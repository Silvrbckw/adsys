package adsysservice

import (
	"github.com/sirupsen/logrus"
	"github.com/ubuntu/adsys"
	"github.com/ubuntu/adsys/internal/daemon"
	"github.com/ubuntu/adsys/internal/grpc/connectionnotify"
	"github.com/ubuntu/adsys/internal/grpc/interceptorschain"
	log "github.com/ubuntu/adsys/internal/grpc/logstreamer"
	"google.golang.org/grpc"
)

// Service is used to implement adsys.ServiceServer.
type Service struct {
	adsys.UnimplementedServiceServer
	logger *logrus.Logger
}

// RegisterGRPCServer registers our service with the new interceptor chains.
// It will notify the daemon of any new connection
func (s *Service) RegisterGRPCServer(d *daemon.Daemon) *grpc.Server {
	s.logger = logrus.StandardLogger()
	srv := grpc.NewServer(grpc.StreamInterceptor(
		interceptorschain.StreamServer(
			log.StreamServerInterceptor(s.logger),
			connectionnotify.StreamServerInterceptor(d),
		)))
	adsys.RegisterServiceServer(srv, s)
	return srv
}