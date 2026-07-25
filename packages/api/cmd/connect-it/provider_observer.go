package main

import (
	"context"
	"errors"
	"io"
	"log"
	"log/slog"
	"time"

	"github.com/memohai/connect-it/packages/core/providerkit"
)

const providerObserverShutdownTimeout = 5 * time.Second

var errProviderObserverWriterRequired = errors.New(
	"provider observer writer is required",
)

func newProductionProviderObserver(
	writer io.Writer,
) (*providerkit.AsyncObserver, error) {
	return newProviderRequestObserver(
		writer,
		providerkit.DefaultObserverQueueCapacity,
	)
}

func newProviderRequestObserver(
	writer io.Writer,
	queueCapacity int,
) (*providerkit.AsyncObserver, error) {
	if writer == nil {
		return nil, errProviderObserverWriterRequired
	}
	logger := slog.New(slog.NewJSONHandler(writer, nil))
	sink := providerkit.ObserverFunc(func(
		ctx context.Context,
		event providerkit.RequestEvent,
	) {
		attrs := []slog.Attr{
			slog.String(
				"connector_type",
				event.Labels.ConnectorType,
			),
			slog.String("provider_host", event.ProviderHost),
			slog.String("method", event.Method),
			slog.Duration("duration", event.Duration),
			slog.Int("attempt", event.Attempt),
			slog.Int("upstream_status", event.UpstreamStatus),
			slog.String("error_code", string(event.ErrorCode)),
			slog.Int64("response_bytes", event.ResponseBytes),
			slog.String(
				"policy_outcome",
				string(event.PolicyOutcome),
			),
		}
		if event.Labels.ToolID != "" {
			attrs = append(
				attrs,
				slog.String("tool_id", event.Labels.ToolID),
			)
		} else {
			attrs = append(
				attrs,
				slog.String("operation", event.Labels.Operation),
			)
		}
		if event.Labels.ConnectionID != "" {
			attrs = append(
				attrs,
				slog.String(
					"connection_id",
					event.Labels.ConnectionID,
				),
			)
		}
		if event.Labels.AuthorizationID != "" {
			attrs = append(
				attrs,
				slog.String(
					"authorization_id",
					event.Labels.AuthorizationID,
				),
			)
		}
		logger.LogAttrs(ctx, slog.LevelInfo, "provider_request", attrs...)
	})
	return providerkit.NewAsyncObserver(sink, queueCapacity)
}

func shutdownProviderObserver(observer *providerkit.AsyncObserver) {
	if observer == nil {
		return
	}
	ctx, cancel := context.WithTimeout(
		context.Background(),
		providerObserverShutdownTimeout,
	)
	defer cancel()
	if err := observer.Close(ctx); err != nil {
		log.Printf("Provider observer 退出失败: %v", err)
	}
	if dropped := observer.Dropped(); dropped > 0 {
		log.Printf("Provider observer 丢弃事件: count=%d", dropped)
	}
}
