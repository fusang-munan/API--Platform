package dataplane

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/starprince1234/Nebula-api/internal/usage"
)

type beichenSocketCall struct {
	reservation   usage.Reservation
	route         usage.CallRoute
	attempt       uuid.UUID
	started       time.Time
	metrics       ResponseMetrics
	responseID    string
	review        string
	modelReported bool
}

func (g *Gateway) proxyBeichenWebSocket(ctx context.Context, client, upstream *websocket.Conn, selected route, keyID uuid.UUID, first usage.Reservation, firstBody []byte) {
	adapter := selected.Binding.Adapter
	var active *beichenSocketCall
	var callMu, clientWriteMu sync.Mutex
	var turnID uuid.UUID
	seen := make(map[string]struct{})
	writeRejection := func(err error) error {
		clientWriteMu.Lock()
		defer clientWriteMu.Unlock()
		return client.WriteJSON(modelCallErrorEvent(err))
	}
	send := func(reservation usage.Reservation, data []byte) error {
		call := &beichenSocketCall{reservation: reservation, route: selected.callRoute(), started: time.Now()}
		call.review = beichenPayloadReview(data, call.route.UpstreamModelName, true)
		call.route.DefinitelyNotSent = true
		field := quotaTokenField("/v1/responses")
		var quotaBudget int64
		if limit := g.quotaTokenLimit(ctx, reservation, call.route, field); limit > 0 {
			payload := jsonObject(data)
			quotaBudget = applyOutputBudget(payload, field, limit)
			var err error
			data, err = json.Marshal(payload)
			if err != nil {
				_ = g.finalizeCall(ctx, reservation, false, "request_rewrite", "Unable to prepare the upstream message.", call.route, usage.CallMetrics{})
				return err
			}
		}
		attempt, err := g.usage.StartAttempt(ctx, reservation, call.route, quotaBudget)
		if err != nil {
			_ = g.finalizeCall(ctx, reservation, false, "dispatch_rejected", "Unable to authorize upstream dispatch.", call.route, usage.CallMetrics{})
			return writeRejection(err)
		}
		if err = g.usage.PrepareDispatch(ctx, reservation.CallID, call.route); err != nil {
			g.finishAttempt(ctx, attempt, call.started, false, nil, "connection", "Unable to record upstream attempt.")
			_ = g.finalizeCall(ctx, reservation, false, "connection", "Unable to record upstream attempt.", call.route, usage.CallMetrics{State: usage.UsageMissing})
			return err
		}
		call.attempt = attempt
		if err = ctx.Err(); err != nil {
			g.finishAttempt(ctx, attempt, call.started, false, nil, "connection", "Request canceled before dispatch.")
			_ = g.finalizeCall(ctx, reservation, false, "connection", "Request canceled before dispatch.", call.route, usage.CallMetrics{State: usage.UsageMissing})
			return err
		}
		// A WebSocket write error can occur after a partial frame write.
		call.route.SentAt = time.Now().UTC()
		call.route.DefinitelyNotSent = false
		active = call
		err = upstream.WriteMessage(websocket.TextMessage, data)
		if err == nil {
			g.markCallSent(ctx, reservation.CallID)
		} else {
			call.route.SentAt = time.Time{}
		}
		turnID = reservation.TurnID
		return err
	}
	finish := func(success bool) {
		if active == nil {
			return
		}
		category, message := "", ""
		if !success {
			category, message = "stream", "Upstream WebSocket response did not complete."
		}
		if active.review == "" && !active.modelReported {
			active.review = beichenModelUnconfirmed
		}
		metrics, review := beichenReviewMetrics(&active.route, active.metrics.finished(!success), active.review)
		if success && review != "" {
			category, message = review, "The response requires billing review."
		}
		_ = g.finalizeCall(ctx, active.reservation, success, category, message, active.route, metrics)
		g.finishAttempt(ctx, active.attempt, active.started, success, nil, category, message)
		active = nil
	}
	if err := send(first, firstBody); err != nil {
		finish(false)
		return
	}
	writeError := func(code, message string) error {
		clientWriteMu.Lock()
		defer clientWriteMu.Unlock()
		return client.WriteJSON(map[string]any{"type": "error", "error": map[string]string{"code": code, "message": message}})
	}
	done := make(chan struct{}, 2)
	go func() {
		defer func() { done <- struct{}{} }()
		for {
			messageType, data, err := client.ReadMessage()
			if err != nil {
				return
			}
			if messageType != websocket.TextMessage {
				return
			}
			switch rawString(jsonObject(data)["type"]) {
			case "response.cancel":
				if upstream.WriteMessage(messageType, data) != nil {
					return
				}
				continue
			case "response.create":
			default:
				if writeError("unsupported_event", "This WebSocket accepts response.create and response.cancel.") != nil {
					return
				}
				continue
			}
			callMu.Lock()
			if len(seen) >= 4096 {
				callMu.Unlock()
				_ = writeError("connection_limit", "Open a new connection to continue.")
				return
			}
			if active != nil {
				callMu.Unlock()
				if writeError("response_in_progress", "Wait for the current response to finish.") != nil {
					return
				}
				continue
			}
			prompt, source := extractPromptFromJSON(data)
			prompt, correlation := turnCorrelation(prompt, extractCallCorrelationFromJSON(data), turnID)
			data, err = rewriteResponsesWebSocketModel(data, selected.Binding.UpstreamModelName)
			if err != nil {
				callMu.Unlock()
				return
			}
			requestID := uuid.Must(uuid.NewV7()).String()
			if turnID == uuid.Nil {
				if id := usage.RequestID(ctx); id != "" {
					requestID = id
				}
			}
			reservation, err := g.usage.Reserve(ctx, keyID, selected.Binding.ModelID, requestID, string(adapter), "/v1/responses", prompt, source, correlation, selected.callRoute())
			if err != nil {
				callMu.Unlock()
				if writeRejection(err) != nil {
					return
				}
				continue
			}
			err = send(reservation, data)
			callMu.Unlock()
			if err != nil {
				return
			}
		}
	}()
	go func() {
		defer func() { done <- struct{}{} }()
		for {
			messageType, data, err := upstream.ReadMessage()
			if err != nil {
				return
			}
			if messageType == websocket.TextMessage {
				payload := jsonObject(data)
				callMu.Lock()
				if active != nil {
					event := rawString(payload["type"])
					response := jsonObject(payload["response"])
					responseID := rawString(response["id"])
					if responseID == "" {
						responseID = rawString(payload["response_id"])
					}
					switch event {
					case "response.created":
						_, replayed := seen[responseID]
						if active.responseID == "" && responseID != "" && !replayed && len(seen) < 4096 {
							active.responseID = responseID
							seen[responseID] = struct{}{}
							active.metrics = parseJSONResponseMetrics(adapter, data)
						} else if responseID != "" && active.responseID != "" && active.responseID != responseID && !replayed {
							active.review = beichenUnsupportedPrice
						}
					case "response.completed", "response.done", "response.incomplete", "response.failed":
						// Terminal usage is authoritative for this serialized call.
						if active.responseID != "" && active.responseID == responseID {
							if active.review == "" {
								active.review = beichenPayloadReview(data, active.route.UpstreamModelName, false)
								active.modelReported = active.modelReported || beichenModelReported(payload, active.route.UpstreamModelName)
							}
							active.metrics = parseJSONResponseMetrics(adapter, data)
							status := rawString(response["status"])
							finish((event == "response.completed" || event == "response.done") && (status == "" || status == "completed"))
						}
					}
					if active != nil && active.review == "" && (responseID == "" || active.responseID == responseID) {
						active.review = beichenPayloadReview(data, active.route.UpstreamModelName, false)
						active.modelReported = active.modelReported || beichenModelReported(payload, active.route.UpstreamModelName)
					}
				}
				callMu.Unlock()
			}
			clientWriteMu.Lock()
			err = client.WriteMessage(messageType, data)
			clientWriteMu.Unlock()
			if err != nil {
				return
			}
		}
	}()
	remaining := 2
	select {
	case <-done:
		remaining--
	case <-ctx.Done():
	}
	_ = client.Close()
	_ = upstream.Close()
	for range remaining {
		<-done
	}
	finish(false)
}
