package reports

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/ids"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Claim struct {
	ID                     string
	TenantID               string
	MerchantID             string
	Format                 string
	PeriodStart            time.Time
	PeriodEnd              time.Time
	SnapshotLedgerSequence int64
	SnapshotFenceSequence  int64
	SnapshotCutoff         time.Time
	AttemptCount           int
	LeaseToken             string
}

type Worker struct {
	pool         *pgxpool.Pool
	store        ObjectStore
	privateKey   ed25519.PrivateKey
	signingKeyID string
	workerID     string
	lease        time.Duration
	maxAttempts  int
	baseBackoff  time.Duration
	maxBackoff   time.Duration
	temporaryDir string
	maxEntries   int
	maxBytes     int64
	lastPollUnix atomic.Int64
	now          func() time.Time
}

type WorkerConfig struct {
	Lease              time.Duration
	MaxAttempts        int
	BaseBackoff        time.Duration
	MaxBackoff         time.Duration
	TemporaryDirectory string
	MaxEntries         int
	MaxObjectBytes     int64
}

func NewWorker(pool *pgxpool.Pool, store ObjectStore, privateKey ed25519.PrivateKey, signingKeyID, workerID string, config WorkerConfig) (*Worker, error) {
	if pool == nil || store == nil || len(privateKey) != ed25519.PrivateKeySize || signingKeyID == "" || len(signingKeyID) > 128 || workerID == "" || len(workerID) > 128 || config.Lease < 30*time.Second || config.Lease > 15*time.Minute || config.MaxAttempts < 1 || config.MaxAttempts > 20 || config.BaseBackoff < time.Second || config.BaseBackoff > time.Hour || config.MaxBackoff < config.BaseBackoff || config.MaxBackoff > 24*time.Hour || config.MaxEntries < 1 || config.MaxEntries > 10_000_000 || config.MaxObjectBytes < 1<<20 || config.MaxObjectBytes > 5<<30 {
		return nil, errors.New("valid reconciliation worker dependencies and identity are required")
	}
	if config.TemporaryDirectory != "" {
		info, err := os.Stat(config.TemporaryDirectory)
		if err != nil || !info.IsDir() {
			return nil, errors.New("reconciliation temporary directory must exist")
		}
	}
	return &Worker{pool: pool, store: store, privateKey: append(ed25519.PrivateKey(nil), privateKey...), signingKeyID: signingKeyID, workerID: workerID, lease: config.Lease, maxAttempts: config.MaxAttempts, baseBackoff: config.BaseBackoff, maxBackoff: config.MaxBackoff, temporaryDir: config.TemporaryDirectory, maxEntries: config.MaxEntries, maxBytes: config.MaxObjectBytes, now: func() time.Time { return time.Now().UTC() }}, nil
}

func (w *Worker) RunOnce(ctx context.Context) (bool, error) {
	w.lastPollUnix.Store(w.now().Unix())
	claim, found, err := w.claim(ctx)
	if err != nil || !found {
		return found, err
	}
	key := objectKey(claim.TenantID, claim.ID)
	workContext, cancelWork := context.WithCancel(ctx)
	renewErrors := make(chan error, 1)
	go w.keepLease(workContext, claim, renewErrors)
	temporary, digest, size, err := w.generate(workContext, claim)
	if err != nil {
		cancelWork()
		<-renewErrors
		code, permanent := "generation_failed", false
		if errors.Is(err, ErrReportQuota) {
			code, permanent = "quota_exceeded", true
		}
		_ = w.fail(ctx, claim, code, permanent)
		return true, err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	promoteErr := w.store.Promote(ctx, key, temporary, digest, size)
	closeErr := temporary.Close()
	if promoteErr != nil {
		cancelWork()
		<-renewErrors
		_ = w.fail(ctx, claim, "storage_failed", false)
		return true, promoteErr
	}
	if closeErr != nil {
		cancelWork()
		<-renewErrors
		_ = w.fail(ctx, claim, "storage_failed", false)
		return true, closeErr
	}
	signature := ed25519.Sign(w.privateKey, signatureMessage(claim.ID, strconv.FormatInt(claim.SnapshotLedgerSequence, 10), digest))
	if err = w.complete(ctx, claim, key, size, digest, signature); err != nil {
		cancelWork()
		<-renewErrors
		return true, err
	}
	cancelWork()
	<-renewErrors
	return true, nil
}

func (w *Worker) keepLease(ctx context.Context, claim Claim, result chan<- error) {
	ticker := time.NewTicker(w.lease / 3)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			result <- nil
			return
		case <-ticker.C:
			command, err := w.pool.Exec(ctx, `UPDATE reconciliation_reports SET locked_until=clock_timestamp()+$1::double precision*interval '1 second',updated_at=clock_timestamp() WHERE id=$2 AND tenant_id=$3 AND merchant_id=$4 AND status='processing' AND lease_token=$5 AND locked_by=$6 AND locked_until>=clock_timestamp()`, w.lease.Seconds(), claim.ID, claim.TenantID, claim.MerchantID, claim.LeaseToken, w.workerID)
			if err != nil || command.RowsAffected() != 1 {
				if err == nil {
					err = errors.New("reconciliation worker lost its fenced lease")
				}
				result <- err
				return
			}
		}
	}
}

func (w *Worker) claim(ctx context.Context) (claim Claim, found bool, err error) {
	leaseToken, err := ids.New()
	if err != nil {
		return claim, false, err
	}
	err = pgx.BeginTxFunc(ctx, w.pool, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `WITH candidate AS (
SELECT id FROM reconciliation_reports WHERE status IN('queued','retry') AND next_attempt_at<=clock_timestamp() AND (locked_until IS NULL OR locked_until<clock_timestamp()) ORDER BY next_attempt_at,id FOR UPDATE SKIP LOCKED LIMIT 1
), claimed AS (
UPDATE reconciliation_reports report SET status='processing',attempt_count=attempt_count+1,locked_by=$1,locked_until=clock_timestamp()+$2::double precision*interval '1 second',lease_token=$3,updated_at=clock_timestamp(),version=version+1 FROM candidate WHERE report.id=candidate.id
RETURNING report.id::text,report.tenant_id::text,report.merchant_id::text,report.format,report.period_start,report.period_end,report.snapshot_ledger_sequence,report.snapshot_fence_sequence,report.snapshot_cutoff,report.attempt_count,report.lease_token::text
) SELECT * FROM claimed`, w.workerID, w.lease.Seconds(), leaseToken)
		err := row.Scan(&claim.ID, &claim.TenantID, &claim.MerchantID, &claim.Format, &claim.PeriodStart, &claim.PeriodEnd, &claim.SnapshotLedgerSequence, &claim.SnapshotFenceSequence, &claim.SnapshotCutoff, &claim.AttemptCount, &claim.LeaseToken)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err == nil {
			found = true
		}
		return err
	})
	return claim, found, err
}

type reportHeader struct {
	RecordType             string    `json:"record_type"`
	SchemaVersion          string    `json:"schema_version"`
	ReportID               string    `json:"report_id"`
	MerchantID             string    `json:"merchant_id"`
	Format                 string    `json:"format"`
	PeriodStart            time.Time `json:"period_start"`
	PeriodEnd              time.Time `json:"period_end"`
	SnapshotLedgerSequence string    `json:"snapshot_ledger_sequence"`
	SnapshotCutoff         time.Time `json:"snapshot_cutoff"`
}

type reportEntry struct {
	RecordType        string    `json:"record_type"`
	LedgerSequence    string    `json:"ledger_sequence"`
	TransactionID     string    `json:"transaction_id"`
	EntrySequence     string    `json:"entry_sequence"`
	BusinessType      string    `json:"business_type"`
	BusinessReference string    `json:"business_reference"`
	EffectiveAt       time.Time `json:"effective_at"`
	BookedAt          time.Time `json:"booked_at"`
	AccountCode       string    `json:"account_code"`
	AccountType       string    `json:"account_type"`
	AssetID           string    `json:"asset_id"`
	Direction         string    `json:"direction"`
	AmountAtomic      string    `json:"amount_atomic"`
}

type reportTotal struct {
	AssetID      string `json:"asset_id"`
	DebitAtomic  string `json:"debit_atomic"`
	CreditAtomic string `json:"credit_atomic"`
}

type reportFooter struct {
	RecordType string        `json:"record_type"`
	EntryCount string        `json:"entry_count"`
	Totals     []reportTotal `json:"totals"`
}

func (w *Worker) generate(ctx context.Context, claim Claim) (*os.File, []byte, int64, error) {
	if claim.Format != "jsonl_v1" {
		return nil, nil, 0, errors.New("unsupported reconciliation format")
	}
	file, err := os.CreateTemp(w.temporaryDir, ".reconciliation-*.tmp")
	if err != nil {
		return nil, nil, 0, err
	}
	if err = file.Chmod(0o600); err != nil {
		file.Close()
		os.Remove(file.Name())
		return nil, nil, 0, err
	}
	cleanup := func(generationErr error) (*os.File, []byte, int64, error) {
		file.Close()
		os.Remove(file.Name())
		return nil, nil, 0, generationErr
	}
	hash := sha256.New()
	counting := &countWriter{writer: io.MultiWriter(file, hash), maximum: w.maxBytes}
	encoder := json.NewEncoder(counting)
	encoder.SetEscapeHTML(false)
	if err = encoder.Encode(reportHeader{RecordType: "header", SchemaVersion: "1", ReportID: claim.ID, MerchantID: claim.MerchantID, Format: claim.Format, PeriodStart: claim.PeriodStart, PeriodEnd: claim.PeriodEnd, SnapshotLedgerSequence: strconv.FormatInt(claim.SnapshotLedgerSequence, 10), SnapshotCutoff: claim.SnapshotCutoff}); err != nil {
		return cleanup(err)
	}
	rows, err := w.pool.Query(ctx, `SELECT ledger_tx.ledger_sequence,ledger_tx.id::text,entry.sequence,ledger_tx.business_type,ledger_tx.business_reference,ledger_tx.effective_at,ledger_tx.booked_at,account.account_code,account.account_type,entry.asset_id,entry.direction::text,entry.amount_atomic::text
FROM ledger_transactions ledger_tx JOIN ledger_entries entry ON entry.transaction_id=ledger_tx.id AND entry.tenant_id=ledger_tx.tenant_id JOIN ledger_accounts account ON account.id=entry.account_id AND account.tenant_id=entry.tenant_id AND account.asset_id=entry.asset_id
WHERE ledger_tx.tenant_id=$1 AND account.merchant_id=$2 AND ledger_tx.ledger_sequence<=$3 AND ledger_tx.effective_at>=$4 AND ledger_tx.effective_at<$5
ORDER BY ledger_tx.ledger_sequence,entry.sequence LIMIT $6`, claim.TenantID, claim.MerchantID, claim.SnapshotFenceSequence, claim.PeriodStart, claim.PeriodEnd, w.maxEntries+1)
	if err != nil {
		return cleanup(err)
	}
	defer rows.Close()
	type pair struct{ debit, credit uintString }
	totals := map[string]pair{}
	var count int64
	for rows.Next() {
		if count >= int64(w.maxEntries) {
			return cleanup(ErrReportQuota)
		}
		var ledgerSequence int64
		var entrySequence int
		var entry reportEntry
		if err = rows.Scan(&ledgerSequence, &entry.TransactionID, &entrySequence, &entry.BusinessType, &entry.BusinessReference, &entry.EffectiveAt, &entry.BookedAt, &entry.AccountCode, &entry.AccountType, &entry.AssetID, &entry.Direction, &entry.AmountAtomic); err != nil {
			return cleanup(err)
		}
		entry.RecordType = "ledger_entry"
		entry.LedgerSequence = strconv.FormatInt(ledgerSequence, 10)
		entry.EntrySequence = strconv.Itoa(entrySequence)
		if err = encoder.Encode(entry); err != nil {
			return cleanup(err)
		}
		amount, err := parseUintString(entry.AmountAtomic)
		if err != nil {
			return cleanup(err)
		}
		total := totals[entry.AssetID]
		if entry.Direction == "debit" {
			total.debit = addUintString(total.debit, amount)
		} else if entry.Direction == "credit" {
			total.credit = addUintString(total.credit, amount)
		} else {
			return cleanup(errors.New("invalid ledger direction"))
		}
		totals[entry.AssetID] = total
		count++
	}
	if err = rows.Err(); err != nil {
		return cleanup(err)
	}
	assets := make([]string, 0, len(totals))
	for asset := range totals {
		assets = append(assets, asset)
	}
	sort.Strings(assets)
	footer := reportFooter{RecordType: "footer", EntryCount: strconv.FormatInt(count, 10), Totals: make([]reportTotal, 0, len(assets))}
	for _, asset := range assets {
		total := totals[asset]
		footer.Totals = append(footer.Totals, reportTotal{AssetID: asset, DebitAtomic: total.debit.String(), CreditAtomic: total.credit.String()})
	}
	if err = encoder.Encode(footer); err != nil {
		return cleanup(err)
	}
	return file, hash.Sum(nil), counting.count, nil
}

func (w *Worker) complete(ctx context.Context, claim Claim, key string, size int64, digest, signature []byte) error {
	command, err := w.pool.Exec(ctx, `UPDATE reconciliation_reports SET status='ready',object_key=$1,object_size_bytes=$2,object_sha256=$3,signature=$4,signing_key_id=$5,completed_at=clock_timestamp(),updated_at=clock_timestamp(),locked_by=NULL,locked_until=NULL,lease_token=NULL,last_error_code=NULL,version=version+1 WHERE id=$6 AND tenant_id=$7 AND merchant_id=$8 AND status='processing' AND lease_token=$9 AND locked_by=$10 AND locked_until>=clock_timestamp()`, key, size, digest, signature, w.signingKeyID, claim.ID, claim.TenantID, claim.MerchantID, claim.LeaseToken, w.workerID)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return errors.New("reconciliation worker lost its fenced lease")
	}
	return nil
}

func (w *Worker) fail(ctx context.Context, claim Claim, code string, permanent bool) error {
	status := "retry"
	if permanent || claim.AttemptCount >= w.maxAttempts {
		status = "dead_letter"
	}
	delay := w.baseBackoff * time.Duration(1<<min(claim.AttemptCount-1, 16))
	if delay > w.maxBackoff {
		delay = w.maxBackoff
	}
	command, err := w.pool.Exec(ctx, `UPDATE reconciliation_reports SET status=$1,last_error_code=$2,next_attempt_at=clock_timestamp()+$3::double precision*interval '1 second',updated_at=clock_timestamp(),locked_by=NULL,locked_until=NULL,lease_token=NULL,version=version+1 WHERE id=$4 AND tenant_id=$5 AND merchant_id=$6 AND status='processing' AND lease_token=$7 AND locked_by=$8`, status, code, delay.Seconds(), claim.ID, claim.TenantID, claim.MerchantID, claim.LeaseToken, w.workerID)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return errors.New("reconciliation worker lost its fenced lease")
	}
	return nil
}

type Health struct {
	DatabaseReady  bool       `json:"database_ready"`
	Ready          bool       `json:"ready"`
	PendingCount   string     `json:"pending_count"`
	OldestQueuedAt *time.Time `json:"oldest_queued_at,omitempty"`
	LastSuccessAt  *time.Time `json:"last_success_at,omitempty"`
	LastPollAt     *time.Time `json:"last_poll_at,omitempty"`
}

func (w *Worker) Health(ctx context.Context, maximumQueueAge, maximumPollAge time.Duration) (Health, error) {
	result := Health{DatabaseReady: false, PendingCount: "0"}
	if maximumQueueAge < 30*time.Second || maximumQueueAge > 24*time.Hour || maximumPollAge < time.Second || maximumPollAge > 10*time.Minute {
		return result, errors.New("maximum queue age is outside bounds")
	}
	var pending int64
	err := w.pool.QueryRow(ctx, `SELECT count(*) FILTER(WHERE status IN('queued','retry','processing')),min(created_at) FILTER(WHERE status IN('queued','retry','processing')),max(completed_at) FILTER(WHERE status='ready') FROM reconciliation_reports`).Scan(&pending, &result.OldestQueuedAt, &result.LastSuccessAt)
	if err != nil {
		return result, err
	}
	result.DatabaseReady = true
	result.PendingCount = strconv.FormatInt(pending, 10)
	if unix := w.lastPollUnix.Load(); unix > 0 {
		poll := time.Unix(unix, 0).UTC()
		result.LastPollAt = &poll
	}
	queueReady := result.OldestQueuedAt == nil || w.now().Sub(result.OldestQueuedAt.UTC()) <= maximumQueueAge
	pollReady := result.LastPollAt != nil && w.now().Sub(result.LastPollAt.UTC()) <= maximumPollAge
	result.Ready = queueReady && pollReady
	return result, nil
}

type countWriter struct {
	writer  io.Writer
	count   int64
	maximum int64
}

func (w *countWriter) Write(data []byte) (int, error) {
	if int64(len(data)) > w.maximum-w.count {
		return 0, ErrReportQuota
	}
	written, err := w.writer.Write(data)
	w.count += int64(written)
	return written, err
}

var ErrReportQuota = errors.New("reconciliation report exceeds admitted quota")

// uintString keeps report aggregation exact without accepting signs, decimals,
// exponent notation, or leading zero aliases.
type uintString []byte

func parseUintString(value string) (uintString, error) {
	if value == "" || value != "0" && value[0] == '0' {
		return nil, errors.New("invalid uint string")
	}
	for _, character := range []byte(value) {
		if character < '0' || character > '9' {
			return nil, errors.New("invalid uint string")
		}
	}
	return uintString(value), nil
}

func addUintString(left, right uintString) uintString {
	if len(left) == 0 {
		left = uintString("0")
	}
	maxLength := len(left)
	if len(right) > maxLength {
		maxLength = len(right)
	}
	result := make([]byte, maxLength+1)
	carry := byte(0)
	for offset := 0; offset < maxLength; offset++ {
		leftDigit, rightDigit := byte(0), byte(0)
		if index := len(left) - 1 - offset; index >= 0 {
			leftDigit = left[index] - '0'
		}
		if index := len(right) - 1 - offset; index >= 0 {
			rightDigit = right[index] - '0'
		}
		sum := leftDigit + rightDigit + carry
		result[maxLength-offset] = '0' + sum%10
		carry = sum / 10
	}
	if carry > 0 {
		result[0] = '0' + carry
		return uintString(result)
	}
	return uintString(result[1:])
}

func (v uintString) String() string {
	if len(v) == 0 {
		return "0"
	}
	return string(v)
}

func min(left, right int) int {
	if left < right {
		return left
	}
	return right
}

func DigestHex(value []byte) string { return hex.EncodeToString(value) }
func FormatError(err error) error   { return fmt.Errorf("reconciliation worker: %w", err) }
