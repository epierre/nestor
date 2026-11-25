package store

import (
	"context"
	"errors"
	"testing"
)

func TestConversationContext(t *testing.T) {
	tests := []struct {
		name    string
		ctx     context.Context
		wantID  int64
		wantErr error
	}{
		{
			name:   "positive ID round-trips",
			ctx:    WithConversation(context.Background(), 42),
			wantID: 42,
		},
		{
			name:    "missing conversation ID",
			ctx:     context.Background(),
			wantErr: ErrNoConversation,
		},
		{
			name:    "zero conversation ID rejected",
			ctx:     WithConversation(context.Background(), 0),
			wantErr: ErrNoConversation,
		},
		{
			name:    "negative conversation ID rejected",
			ctx:     WithConversation(context.Background(), -5),
			wantErr: ErrNoConversation,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ConversationFrom(tt.ctx)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("ConversationFrom() err = %v, want %v", err, tt.wantErr)
			}
			if got != tt.wantID {
				t.Errorf("ConversationFrom() = %d, want %d", got, tt.wantID)
			}
		})
	}
}
