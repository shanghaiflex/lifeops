package finance

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"lifeops/internal/store"
)

type Ingestor struct {
	store *store.Store
}

func NewIngestor(store *store.Store) *Ingestor {
	return &Ingestor{store: store}
}

func (i *Ingestor) IngestDirectory(ctx context.Context, dir string) (int, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, fmt.Errorf("read finance dir: %w", err)
	}
	var total int
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if strings.HasSuffix(strings.ToLower(entry.Name()), ".csv") {
			count, err := i.ingestFile(ctx, filepath.Join(dir, entry.Name()))
			if err != nil {
				return total, err
			}
			total += count
		}
	}
	if err := i.store.RebuildFinanceAggregates(ctx); err != nil {
		return total, err
	}
	return total, nil
}

func (i *Ingestor) ingestFile(ctx context.Context, path string) (int, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, fmt.Errorf("open %s: %w", path, err)
	}
	defer file.Close()

	r := csv.NewReader(file)
	r.FieldsPerRecord = -1
	headers, err := r.Read()
	if err != nil {
		return 0, fmt.Errorf("read headers: %w", err)
	}
	mapper := newMapper(headers)
	rowNum := 0
	var raws []store.FinanceRaw
	var txs []store.FinanceTransaction
	for {
		record, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return 0, fmt.Errorf("read row: %w", err)
		}
		rowNum++
		payload := map[string]string{}
		for idx, header := range headers {
			if idx < len(record) {
				payload[header] = record[idx]
			}
		}
		rawBytes, _ := json.Marshal(payload)
		raws = append(raws, store.FinanceRaw{SourceFile: filepath.Base(path), RowNum: rowNum, Payload: rawBytes})
		parsed, err := mapper.parse(record)
		if err != nil {
			continue
		}
		txs = append(txs, parsed)
	}
	ids, err := i.store.InsertFinanceRaw(ctx, raws)
	if err != nil {
		return 0, err
	}
	for idx := range txs {
		if idx < len(ids) {
			txs[idx].RawID = ids[idx]
		}
	}
	if err := i.store.InsertFinanceTransactions(ctx, txs); err != nil {
		return 0, err
	}
	return len(txs), nil
}

type mapper struct {
	idxDate        int
	idxAmount      int
	idxCurrency    int
	idxDescription int
	idxCategory    int
	idxMerchant    int
	idxType        int
}

func newMapper(headers []string) mapper {
	m := mapper{idxDate: -1, idxAmount: -1, idxCurrency: -1, idxDescription: -1, idxCategory: -1, idxMerchant: -1, idxType: -1}
	for i, header := range headers {
		h := strings.ToLower(strings.TrimSpace(header))
		switch {
		case h == "date" || h == "transaction_date" || h == "booking_date":
			m.idxDate = i
		case strings.Contains(h, "amount"):
			m.idxAmount = i
		case h == "currency" || h == "ccy":
			m.idxCurrency = i
		case h == "description" || h == "details" || h == "memo":
			m.idxDescription = i
		case h == "category" || h == "cat":
			m.idxCategory = i
		case h == "merchant" || h == "payee":
			m.idxMerchant = i
		case h == "type":
			m.idxType = i
		}
	}
	return m
}

func (m mapper) parse(record []string) (store.FinanceTransaction, error) {
	var tx store.FinanceTransaction
	if m.idxDate < 0 || m.idxAmount < 0 || m.idxDescription < 0 {
		return tx, fmt.Errorf("missing required columns")
	}
	dateStr := strings.TrimSpace(record[m.idxDate])
	parsedDate, err := parseDate(dateStr)
	if err != nil {
		return tx, err
	}
	amountStr := strings.ReplaceAll(record[m.idxAmount], ",", ".")
	amount, err := strconv.ParseFloat(strings.TrimSpace(amountStr), 64)
	if err != nil {
		return tx, err
	}
	tx.Date = parsedDate
	tx.Amount = amount
	tx.Description = valueAt(record, m.idxDescription)
	tx.Currency = valueAt(record, m.idxCurrency)
	if tx.Currency == "" {
		tx.Currency = "EUR"
	}
	tx.Category = valueAt(record, m.idxCategory)
	tx.Merchant = valueAt(record, m.idxMerchant)
	typeVal := strings.ToLower(valueAt(record, m.idxType))
	tx.IsSub = strings.Contains(typeVal, "subscription") || strings.Contains(strings.ToLower(tx.Description), "subscription")
	return tx, nil
}

func parseDate(value string) (time.Time, error) {
	layouts := []string{"2006-01-02", "02/01/2006", "01/02/2006"}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, value); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid date %s", value)
}

func valueAt(record []string, idx int) string {
	if idx >= 0 && idx < len(record) {
		return strings.TrimSpace(record[idx])
	}
	return ""
}
