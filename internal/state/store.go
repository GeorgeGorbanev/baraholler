package state

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	gotdbbolt "github.com/gotd/contrib/bbolt"
	"go.etcd.io/bbolt"
)

var (
	deliveryBucket      = []byte("baraholler-deliveries-v1")
	pendingBucket       = []byte("baraholler-pending-deliveries-v1")
	sourceMessageBucket = []byte("baraholler-source-messages-v1")
	channelAccessBucket = []byte("baraholler-channel-access-v1")
	userAccessBucket    = []byte("baraholler-user-access-v1")
	peerBucket          = []byte("baraholler-peers-v1")
)

type DeliveryKey struct {
	Rule       string `json:"rule"`
	SourceID   int64  `json:"source_id"`
	MessageID  int    `json:"message_id"`
	NotifierID string `json:"notifier_id"`
}

type PendingDelivery struct {
	Key                DeliveryKey `json:"key"`
	MessageExcerpt     string      `json:"message_excerpt"`
	MatchedTerms       []string    `json:"matched_terms"`
	NoForwards         bool        `json:"no_forwards"`
	NotifierCheckpoint string      `json:"notifier_checkpoint,omitempty"`
	Attempts           int         `json:"attempts"`
	NextAttemptTime    time.Time   `json:"next_attempt_time"`
}

// SourceMessageID returns the greatest message ID observed for a source.
func (s *Store) SourceMessageID(ctx context.Context, sourceID int64) (int, bool, error) {
	if err := ctx.Err(); err != nil {
		return 0, false, err
	}
	key := int64Key(sourceID)
	var (
		messageID int
		found     bool
	)
	err := s.db.View(func(tx *bbolt.Tx) error {
		bucket := tx.Bucket(sourceMessageBucket)
		if bucket == nil {
			return nil
		}
		encoded := bucket.Get(key)
		if encoded == nil {
			return nil
		}
		if len(encoded) != 8 {
			return errors.New("source message id invalid: encoded value length")
		}
		value := binary.BigEndian.Uint64(encoded)
		if value > uint64(^uint(0)>>1) {
			return errors.New("source message id invalid: integer overflow")
		}
		messageID = int(value)
		found = true
		return nil
	})
	if err != nil {
		return 0, false, fmt.Errorf("source message id read failed: %w", err)
	}
	return messageID, found, nil
}

// AdvanceSourceMessageID stores messageID when it is greater than the current value.
func (s *Store) AdvanceSourceMessageID(ctx context.Context, sourceID int64, messageID int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if messageID < 0 {
		return errors.New("source message id invalid: negative value")
	}
	key := int64Key(sourceID)
	encoded := make([]byte, 8)
	binary.BigEndian.PutUint64(encoded, uint64(messageID))
	if err := s.db.Update(func(tx *bbolt.Tx) error {
		bucket, err := tx.CreateBucketIfNotExists(sourceMessageBucket)
		if err != nil {
			return err
		}
		current := bucket.Get(key)
		if len(current) == 8 && binary.BigEndian.Uint64(current) >= uint64(messageID) {
			return nil
		}
		return bucket.Put(key, encoded)
	}); err != nil {
		return fmt.Errorf("source message id write failed: %w", err)
	}
	return nil
}

type Store struct {
	db          *bbolt.DB
	updateState *gotdbbolt.State
	peers       *gotdbbolt.PeerStorage
}

func Open(path string) (*Store, error) {
	directory := filepath.Dir(path)
	_, statErr := os.Stat(directory)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, fmt.Errorf("state directory creation failed: %w", err)
	}
	if errors.Is(statErr, os.ErrNotExist) {
		if err := os.Chmod(directory, 0o700); err != nil {
			return nil, fmt.Errorf("state directory permission update failed: %w", err)
		}
	}

	db, err := bbolt.Open(path, 0o600, &bbolt.Options{})
	if err != nil {
		return nil, fmt.Errorf("state database open failed: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("state database permission update failed: %w", err)
	}
	if err := db.Update(func(tx *bbolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists(peerBucket)
		return err
	}); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("peer storage initialization failed: %w", err)
	}

	return &Store{
		db:          db,
		updateState: gotdbbolt.NewStateStorage(db),
		peers:       gotdbbolt.NewPeerStorage(db, peerBucket),
	}, nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) UpdateState() *gotdbbolt.State {
	return s.updateState
}

func (s *Store) PeerStorage() *gotdbbolt.PeerStorage {
	return s.peers
}

func (s *Store) Delivered(ctx context.Context, key DeliveryKey) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	encoded, err := json.Marshal(key)
	if err != nil {
		return false, fmt.Errorf("delivery key encoding failed: %w", err)
	}

	var delivered bool
	err = s.db.View(func(tx *bbolt.Tx) error {
		bucket := tx.Bucket(deliveryBucket)
		if bucket == nil {
			return nil
		}
		delivered = bucket.Get(encoded) != nil
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("delivery state read failed: %w", err)
	}
	return delivered, nil
}

func (s *Store) MarkDelivered(ctx context.Context, key DeliveryKey) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	encoded, err := json.Marshal(key)
	if err != nil {
		return fmt.Errorf("delivery key encoding failed: %w", err)
	}
	if err := s.db.Update(func(tx *bbolt.Tx) error {
		bucket, err := tx.CreateBucketIfNotExists(deliveryBucket)
		if err != nil {
			return err
		}
		return bucket.Put(encoded, []byte{1})
	}); err != nil {
		return fmt.Errorf("delivery state write failed: %w", err)
	}
	return nil
}

func (s *Store) PendingDelivery(ctx context.Context, key DeliveryKey) (PendingDelivery, bool, error) {
	if err := ctx.Err(); err != nil {
		return PendingDelivery{}, false, err
	}
	encodedKey, err := json.Marshal(key)
	if err != nil {
		return PendingDelivery{}, false, fmt.Errorf("pending delivery key encoding failed: %w", err)
	}

	var (
		pending PendingDelivery
		found   bool
	)
	err = s.db.View(func(tx *bbolt.Tx) error {
		bucket := tx.Bucket(pendingBucket)
		if bucket == nil {
			return nil
		}
		encoded := bucket.Get(encodedKey)
		if encoded == nil {
			return nil
		}
		if err := json.Unmarshal(encoded, &pending); err != nil {
			return fmt.Errorf("pending delivery decoding failed: %w", err)
		}
		found = true
		return nil
	})
	if err != nil {
		return PendingDelivery{}, false, fmt.Errorf("pending delivery read failed: %w", err)
	}
	return pending, found, nil
}

func (s *Store) PendingDeliveries(ctx context.Context) ([]PendingDelivery, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var deliveries []PendingDelivery
	if err := s.db.View(func(tx *bbolt.Tx) error {
		bucket := tx.Bucket(pendingBucket)
		if bucket == nil {
			return nil
		}
		return bucket.ForEach(func(_, encoded []byte) error {
			var pending PendingDelivery
			if err := json.Unmarshal(encoded, &pending); err != nil {
				return fmt.Errorf("pending delivery decoding failed: %w", err)
			}
			deliveries = append(deliveries, pending)
			return nil
		})
	}); err != nil {
		return nil, fmt.Errorf("pending deliveries list failed: %w", err)
	}
	return deliveries, nil
}

func (s *Store) SavePendingDelivery(ctx context.Context, pending PendingDelivery) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	encodedKey, err := json.Marshal(pending.Key)
	if err != nil {
		return fmt.Errorf("pending delivery key encoding failed: %w", err)
	}
	encoded, err := json.Marshal(pending)
	if err != nil {
		return fmt.Errorf("pending delivery encoding failed: %w", err)
	}
	if err := s.db.Update(func(tx *bbolt.Tx) error {
		bucket, err := tx.CreateBucketIfNotExists(pendingBucket)
		if err != nil {
			return err
		}
		return bucket.Put(encodedKey, encoded)
	}); err != nil {
		return fmt.Errorf("pending delivery write failed: %w", err)
	}
	return nil
}

func (s *Store) DeletePendingDelivery(ctx context.Context, key DeliveryKey) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	encodedKey, err := json.Marshal(key)
	if err != nil {
		return fmt.Errorf("pending delivery key encoding failed: %w", err)
	}
	if err := s.db.Update(func(tx *bbolt.Tx) error {
		bucket := tx.Bucket(pendingBucket)
		if bucket == nil {
			return nil
		}
		return bucket.Delete(encodedKey)
	}); err != nil {
		return fmt.Errorf("pending delivery deletion failed: %w", err)
	}
	return nil
}

func (s *Store) SetChannelAccessHash(ctx context.Context, userID, channelID, accessHash int64) error {
	return s.setAccessHash(ctx, channelAccessBucket, userID, channelID, accessHash)
}

func (s *Store) GetChannelAccessHash(ctx context.Context, userID, channelID int64) (int64, bool, error) {
	return s.getAccessHash(ctx, channelAccessBucket, userID, channelID)
}

func (s *Store) SetUserAccessHash(ctx context.Context, userID, targetUserID, accessHash int64) error {
	return s.setAccessHash(ctx, userAccessBucket, userID, targetUserID, accessHash)
}

func (s *Store) GetUserAccessHash(ctx context.Context, userID, targetUserID int64) (int64, bool, error) {
	return s.getAccessHash(ctx, userAccessBucket, userID, targetUserID)
}

func (s *Store) setAccessHash(ctx context.Context, bucketName []byte, ownerID, peerID, accessHash int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	key := accessKey(ownerID, peerID)
	value := make([]byte, 8)
	binary.BigEndian.PutUint64(value, uint64(accessHash))
	if err := s.db.Update(func(tx *bbolt.Tx) error {
		bucket, err := tx.CreateBucketIfNotExists(bucketName)
		if err != nil {
			return err
		}
		return bucket.Put(key, value)
	}); err != nil {
		return fmt.Errorf("access hash write failed: %w", err)
	}
	return nil
}

func (s *Store) getAccessHash(ctx context.Context, bucketName []byte, ownerID, peerID int64) (int64, bool, error) {
	if err := ctx.Err(); err != nil {
		return 0, false, err
	}
	key := accessKey(ownerID, peerID)
	var (
		value int64
		found bool
	)
	err := s.db.View(func(tx *bbolt.Tx) error {
		bucket := tx.Bucket(bucketName)
		if bucket == nil {
			return nil
		}
		encoded := bucket.Get(key)
		if encoded == nil {
			return nil
		}
		if len(encoded) != 8 {
			return errors.New("access hash invalid: encoded value length")
		}
		value = int64(binary.BigEndian.Uint64(encoded))
		found = true
		return nil
	})
	if err != nil {
		return 0, false, fmt.Errorf("access hash read failed: %w", err)
	}
	return value, found, nil
}

func accessKey(ownerID, peerID int64) []byte {
	key := make([]byte, 16)
	binary.BigEndian.PutUint64(key[:8], uint64(ownerID))
	binary.BigEndian.PutUint64(key[8:], uint64(peerID))
	return key
}

func int64Key(value int64) []byte {
	key := make([]byte, 8)
	binary.BigEndian.PutUint64(key, uint64(value))
	return key
}
