/*
 * Copyright (c) 2026 Brandon Miller-Mumford.
 * Licensed under the MIT License; see LICENSE.
 */
package order

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/bbmumford/plutus/observability"
	"go.opentelemetry.io/otel/attribute"
)

// Transition errors.
var (
	ErrOrderNotFound      = errors.New("order: not found")
	ErrInvalidTransition  = errors.New("order: invalid state transition")
	ErrAlreadyExists      = errors.New("order: already exists")
	ErrInvalidOrderIntent = errors.New("order: invalid order intent")
)

// validTransitions defines the allowed state machine edges.
// Key = current status, Value = set of valid next statuses.
var validTransitions = map[OrderStatus][]OrderStatus{
	OrderPending:   {OrderFunded, OrderCancelled},
	OrderFunded:    {OrderAccepted, OrderDisputed, OrderTimedOut},
	OrderAccepted:  {OrderFulfilled, OrderDisputed, OrderCancelled, OrderTimedOut},
	OrderFulfilled: {OrderCompleted, OrderDisputed},
	OrderDisputed:  {OrderCompleted}, // resolved via ruling -> settled
	// Terminal states: OrderCompleted, OrderCancelled, OrderTimedOut
}

// Order holds the full state of an order including intent, fulfillment, and lifecycle.
type Order struct {
	Intent      OrderIntent            `json:"intent"`
	Fulfillment *FulfillmentCommitment `json:"fulfillment,omitempty"`
	Status      OrderStatus            `json:"status"`
	CreatedAt   time.Time              `json:"createdAt"`
	UpdatedAt   time.Time              `json:"updatedAt"`
	History     []StatusChange         `json:"history"`
}

// StatusChange records a state transition.
type StatusChange struct {
	From      OrderStatus `json:"from"`
	To        OrderStatus `json:"to"`
	Timestamp time.Time   `json:"timestamp"`
	Reason    string      `json:"reason,omitempty"`
}

// OrderManager tracks orders and enforces state machine transitions.
type OrderManager struct {
	mu     sync.RWMutex
	orders map[string]*Order
}

// NewOrderManager creates an empty OrderManager.
func NewOrderManager() *OrderManager {
	return &OrderManager{
		orders: make(map[string]*Order),
	}
}

// CreateOrder validates the OrderIntent signature and creates a new order
// in Pending state.
func (m *OrderManager) CreateOrder(intent OrderIntent) (*Order, error) {
	ctx, span := observability.Tracer("mercury.order").Start(context.Background(), "OrderManager.CreateOrder")
	defer span.End()
	span.SetAttributes(attribute.String(observability.AttrOrderID, intent.ID))

	if err := VerifyOrderIntent(&intent); err != nil {
		wrapped := fmt.Errorf("%w: %v", ErrInvalidOrderIntent, err)
		observability.RecordError(span, wrapped)
		return nil, wrapped
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.orders[intent.ID]; exists {
		observability.RecordError(span, ErrAlreadyExists)
		return nil, ErrAlreadyExists
	}

	now := time.Now()
	o := &Order{
		Intent:    intent,
		Status:    OrderPending,
		CreatedAt: now,
		UpdatedAt: now,
		History: []StatusChange{
			{From: -1, To: OrderPending, Timestamp: now, Reason: "created"},
		},
	}
	m.orders[intent.ID] = o
	observability.OrderCreatedCount().Add(ctx, 1)
	return o, nil
}

// Get returns the order for the given ID, or ErrOrderNotFound.
func (m *OrderManager) Get(orderID string) (*Order, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	o, ok := m.orders[orderID]
	if !ok {
		return nil, ErrOrderNotFound
	}
	return o, nil
}

// transition applies a state change if the transition is valid.
func (m *OrderManager) transition(orderID string, target OrderStatus, reason string) (*Order, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	o, ok := m.orders[orderID]
	if !ok {
		return nil, ErrOrderNotFound
	}

	if !isValidTransition(o.Status, target) {
		return nil, fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, o.Status, target)
	}

	now := time.Now()
	o.History = append(o.History, StatusChange{
		From:      o.Status,
		To:        target,
		Timestamp: now,
		Reason:    reason,
	})
	o.Status = target
	o.UpdatedAt = now
	return o, nil
}

// MarkFunded transitions an order from Pending to Funded after escrow is confirmed.
func (m *OrderManager) MarkFunded(orderID string) (*Order, error) {
	return m.transition(orderID, OrderFunded, "escrow funded")
}

// AcceptOrder transitions from Funded to Accepted. The seller signs a
// FulfillmentCommitment which is attached to the order.
func (m *OrderManager) AcceptOrder(orderID string, fc FulfillmentCommitment) (*Order, error) {
	if err := VerifyFulfillment(&fc); err != nil {
		return nil, fmt.Errorf("order: invalid fulfillment: %w", err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	o, ok := m.orders[orderID]
	if !ok {
		return nil, ErrOrderNotFound
	}
	if !isValidTransition(o.Status, OrderAccepted) {
		return nil, fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, o.Status, OrderAccepted)
	}

	now := time.Now()
	o.Fulfillment = &fc
	o.History = append(o.History, StatusChange{
		From:      o.Status,
		To:        OrderAccepted,
		Timestamp: now,
		Reason:    "seller accepted",
	})
	o.Status = OrderAccepted
	o.UpdatedAt = now
	return o, nil
}

// MarkFulfilled transitions from Accepted to Fulfilled (seller shipped).
func (m *OrderManager) MarkFulfilled(orderID string) (*Order, error) {
	return m.transition(orderID, OrderFulfilled, "seller fulfilled")
}

// Complete transitions from Fulfilled to Completed (buyer confirmed receipt).
func (m *OrderManager) Complete(orderID string) (*Order, error) {
	o, err := m.transition(orderID, OrderCompleted, "buyer confirmed receipt")
	if err == nil {
		observability.OrderSettledCount().Add(context.Background(), 1,
			observability.AttrSet(attribute.String(observability.AttrOrderID, orderID)))
	}
	return o, err
}

// Dispute transitions an order to Disputed. Valid from Funded, Accepted, or Fulfilled.
func (m *OrderManager) Dispute(orderID string, reason string) (*Order, error) {
	return m.transition(orderID, OrderDisputed, reason)
}

// ResolveDispute transitions from Disputed to Completed after arbitration ruling.
func (m *OrderManager) ResolveDispute(orderID string, ruling string) (*Order, error) {
	return m.transition(orderID, OrderCompleted, "dispute resolved: "+ruling)
}

// Cancel transitions from Pending or Accepted to Cancelled.
func (m *OrderManager) Cancel(orderID string, reason string) (*Order, error) {
	return m.transition(orderID, OrderCancelled, reason)
}

// Timeout transitions from Funded to TimedOut (auto-refund).
func (m *OrderManager) Timeout(orderID string) (*Order, error) {
	return m.transition(orderID, OrderTimedOut, "escrow timeout")
}

// List returns all orders. For production use, this would support pagination
// and filtering; for now it returns a snapshot.
func (m *OrderManager) List() []*Order {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]*Order, 0, len(m.orders))
	for _, o := range m.orders {
		result = append(result, o)
	}
	return result
}

// ListByStatus returns all orders with the given status.
func (m *OrderManager) ListByStatus(status OrderStatus) []*Order {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var result []*Order
	for _, o := range m.orders {
		if o.Status == status {
			result = append(result, o)
		}
	}
	return result
}

// isValidTransition checks whether moving from `from` to `to` is allowed.
func isValidTransition(from, to OrderStatus) bool {
	allowed, ok := validTransitions[from]
	if !ok {
		return false // terminal state
	}
	for _, s := range allowed {
		if s == to {
			return true
		}
	}
	return false
}
