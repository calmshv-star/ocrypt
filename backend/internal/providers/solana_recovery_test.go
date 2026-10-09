package providers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/domain"
)

// All addresses and signatures below are synthetic, not retained customer data.
const (
	solanaRecoverySignature = "1111111111111111111111111111111111111111111111111111111111111111"
	solanaRecoveryFrom      = "11111111111111111111111111111111"
	solanaRecoveryTo        = "So11111111111111111111111111111111111111112"
	solanaRecoveryAmount    = "9007199254740993" // Must not round through float64.
)

func solanaRecoveryTransaction(t *testing.T) solanaTransaction {
	t.Helper()
	var transaction solanaTransaction
	raw := `{"slot":7,"blockTime":100,"transaction":{"signatures":["` + solanaRecoverySignature + `"],"message":{"accountKeys":["` + solanaRecoveryFrom + `","` + solanaRecoveryTo + `","` + solanaTokenProgram + `","` + solanaToken2022Program + `"],"instructions":[{"program":"system","programId":"` + solanaRecoveryFrom + `","parsed":{"type":"transfer","info":{"source":"` + solanaRecoveryFrom + `","destination":"` + solanaRecoveryTo + `","lamports":` + solanaRecoveryAmount + `}}}]}},"meta":{"err":null}}`
	if err := json.Unmarshal([]byte(raw), &transaction); err != nil {
		t.Fatal(err)
	}
	return transaction
}

func solanaRecoverySource(assets map[string]SolanaAsset) *SolanaSource {
	return &SolanaSource{chainID: "solana:mainnet", nativeAssetID: "sol", nativeDecimals: 9, assets: assets}
}

func assertSolanaRecoveryNative(t *testing.T, events []domain.TransferEvent) {
	t.Helper()
	if len(events) != 1 {
		t.Fatalf("expected only the native payment, got %+v", events)
	}
	event := events[0]
	wantIdentity := domain.EventIdentity{ChainID: "solana:mainnet", TransactionID: solanaRecoverySignature, EventIndex: "instruction:0", AssetID: "sol", ToAddress: solanaRecoveryTo}
	if event.Identity != wantIdentity || event.FromAddress != solanaRecoveryFrom || event.Amount.String() != solanaRecoveryAmount || event.AssetDecimals != 9 || event.Kind != "native_top_level" {
		t.Fatalf("native identity or exact money changed: %+v", event)
	}
	if event.BlockHeight != 7 || event.BlockHash != solanaRecoveryTo || !event.OnChainTime.Equal(time.Unix(100, 0).UTC()) || event.Status != domain.TransferFinalized || event.Confirmations != 3 || event.EvidenceHash == "" {
		t.Fatalf("native finality evidence changed: %+v", event)
	}
}

func TestSolanaNativeOnlyIgnoresUnrelatedTokenInstructions(t *testing.T) {
	for _, program := range []string{solanaTokenProgram, solanaToken2022Program} {
		t.Run(program, func(t *testing.T) {
			transaction := solanaRecoveryTransaction(t)
			// Missing accounts, owner and amount must be irrelevant without configured tokens.
			instruction := solanaInstruction{Program: "spl-token", ProgramID: program, Parsed: json.RawMessage(`{"type":"transferChecked","info":{}}`)}
			transaction.Transaction.Message.Instructions = append(transaction.Transaction.Message.Instructions, instruction)
			transaction.Meta.InnerInstructions = []solanaInnerGroup{{Index: 1, Instructions: []solanaInstruction{instruction}}}
			events, err := solanaRecoverySource(nil).normalizeSolanaTransaction(transaction, 7, solanaRecoveryTo, time.Unix(100, 0).UTC(), 9)
			if err != nil {
				t.Fatalf("unrelated token instruction blocked native payment: %v", err)
			}
			assertSolanaRecoveryNative(t, events)
		})
	}
}

func TestSolanaNativeOnlyIgnoresUnrelatedMalformedTokenBalances(t *testing.T) {
	for _, location := range []string{"pre", "post"} {
		for _, fault := range []string{"missing owner", "invalid owner", "invalid mint", "out of range account"} {
			t.Run(location+"/"+fault, func(t *testing.T) {
				transaction := solanaRecoveryTransaction(t)
				balance := solanaTokenBalance{AccountIndex: 2, Mint: solanaRecoveryTo, Owner: solanaRecoveryFrom, ProgramID: solanaTokenProgram}
				switch fault {
				case "missing owner":
					balance.Owner = ""
				case "invalid owner":
					balance.Owner = "not-a-public-key"
				case "invalid mint":
					balance.Mint = "not-a-mint"
				case "out of range account":
					balance.AccountIndex = 99
				}
				if location == "pre" {
					transaction.Meta.PreTokenBalances = []solanaTokenBalance{balance}
				} else {
					transaction.Meta.PostTokenBalances = []solanaTokenBalance{balance}
				}
				transaction.Meta.InnerInstructions = []solanaInnerGroup{{Index: 0, Instructions: []solanaInstruction{{Program: "spl-token", ProgramID: solanaToken2022Program, Parsed: json.RawMessage(`{"type":"transfer","info":{}}`)}}}}
				events, err := solanaRecoverySource(map[string]SolanaAsset{}).normalizeSolanaTransaction(transaction, 7, solanaRecoveryTo, time.Unix(100, 0).UTC(), 9)
				if err != nil {
					t.Fatalf("unrelated %s token metadata blocked valid native payment: %v", location, err)
				}
				assertSolanaRecoveryNative(t, events)
			})
		}
	}
}

func TestSolanaConfiguredTokenEvidenceRemainsFailClosed(t *testing.T) {
	for _, program := range []string{solanaTokenProgram, solanaToken2022Program} {
		for _, fault := range []string{"none", "missing source metadata", "missing owner", "invalid mint", "conflicting mint", "parsed mint mismatch", "missing program", "conflicting program", "missing amount", "fractional amount", "zero amount"} {
			t.Run(program+"/"+fault, func(t *testing.T) {
				transaction := solanaRecoveryTransaction(t)
				transaction.Meta.PreTokenBalances = []solanaTokenBalance{{AccountIndex: 2, Mint: solanaRecoveryTo, Owner: solanaRecoveryFrom, ProgramID: program}}
				transaction.Meta.PostTokenBalances = []solanaTokenBalance{{AccountIndex: 3, Mint: solanaRecoveryTo, Owner: solanaRecoveryTo, ProgramID: program}}
				info := map[string]any{"source": solanaTokenProgram, "destination": solanaToken2022Program, "mint": solanaRecoveryTo, "amount": solanaRecoveryAmount}
				switch fault {
				case "missing source metadata":
					transaction.Meta.PreTokenBalances = nil
				case "missing owner":
					transaction.Meta.PostTokenBalances[0].Owner = ""
				case "invalid mint":
					transaction.Meta.PostTokenBalances[0].Mint = "invalid"
				case "conflicting mint":
					transaction.Meta.PostTokenBalances[0].Mint = solanaRecoveryFrom
				case "parsed mint mismatch":
					info["mint"] = solanaRecoveryFrom
				case "missing program":
					transaction.Meta.PreTokenBalances[0].ProgramID = ""
				case "conflicting program":
					transaction.Meta.PostTokenBalances[0].ProgramID = solanaRecoveryFrom
				case "missing amount":
					delete(info, "amount")
				case "fractional amount":
					info["amount"] = "1.5"
				case "zero amount":
					info["amount"] = "0"
				}
				parsed, err := json.Marshal(map[string]any{"type": "transferChecked", "info": info})
				if err != nil {
					t.Fatal(err)
				}
				transaction.Meta.InnerInstructions = []solanaInnerGroup{{Index: 0, Instructions: []solanaInstruction{{Program: "spl-token", ProgramID: program, Parsed: parsed}}}}
				events, err := solanaRecoverySource(map[string]SolanaAsset{solanaRecoveryTo: {AssetID: "configured-token", Decimals: 6}}).normalizeSolanaTransaction(transaction, 7, solanaRecoveryTo, time.Unix(100, 0).UTC(), 9)
				if fault == "none" {
					if err != nil || len(events) != 2 || events[1].Identity.AssetID != "configured-token" || events[1].Amount.String() != solanaRecoveryAmount || events[1].Identity.EventIndex != "instruction:0/inner:0" || events[1].FromAddress != solanaRecoveryFrom || events[1].Identity.ToAddress != solanaRecoveryTo || events[1].AssetDecimals != 6 {
						t.Fatalf("configured token positive control failed: events=%+v err=%v", events, err)
					}
					return
				}
				var providerError *ProviderError
				if !errors.As(err, &providerError) || providerError.Kind != ErrorMalformed || len(events) != 0 {
					t.Fatalf("invalid supported token evidence did not fail closed: events=%+v err=%v", events, err)
				}
			})
		}
	}
}

func TestSolanaConfiguredAssetsIgnoreExplicitUnsupportedTokenInstruction(t *testing.T) {
	assets := map[string]SolanaAsset{
		"EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v": {AssetID: "usdc-solana", Decimals: 6},
		"Es9vMFrzaCERmJfrF4H2FYD4KCoNkY11McCe8BenwNYB": {AssetID: "usdt-solana", Decimals: 6},
	}
	for _, program := range []string{solanaTokenProgram, solanaToken2022Program} {
		t.Run(program, func(t *testing.T) {
			transaction := solanaRecoveryTransaction(t)
			// An explicit unsupported mint identifies this instruction as unrelated.
			// Missing metadata without any mint binding must still fail closed.
			parsed := json.RawMessage(`{"type":"transferChecked","info":{"source":"` + solanaTokenProgram + `","destination":"` + solanaToken2022Program + `","mint":"` + solanaRecoveryTo + `","tokenAmount":{"amount":"42"}}}`)
			transaction.Meta.InnerInstructions = []solanaInnerGroup{{Index: 0, Instructions: []solanaInstruction{{Program: "spl-token", ProgramID: program, Parsed: parsed}}}}
			events, err := solanaRecoverySource(assets).normalizeSolanaTransaction(transaction, 7, solanaRecoveryTo, time.Unix(100, 0).UTC(), 9)
			if err != nil {
				t.Fatalf("explicit unsupported token instruction blocked native payment with configured assets: %v", err)
			}
			assertSolanaRecoveryNative(t, events)
		})
	}
}

func TestSolanaConfiguredAssetsRejectUnboundTokenInstruction(t *testing.T) {
	transaction := solanaRecoveryTransaction(t)
	parsed := json.RawMessage(`{"type":"transfer","info":{"source":"` + solanaTokenProgram + `","destination":"` + solanaToken2022Program + `","amount":"42"}}`)
	transaction.Meta.InnerInstructions = []solanaInnerGroup{{Index: 0, Instructions: []solanaInstruction{{Program: "spl-token", ProgramID: solanaTokenProgram, Parsed: parsed}}}}
	events, err := solanaRecoverySource(map[string]SolanaAsset{solanaRecoveryTo: {AssetID: "configured-token", Decimals: 6}}).normalizeSolanaTransaction(transaction, 7, solanaRecoveryTo, time.Unix(100, 0).UTC(), 9)
	var providerError *ProviderError
	if !errors.As(err, &providerError) || providerError.Kind != ErrorMalformed || len(events) != 0 {
		t.Fatalf("unbound token transfer was guessed unrelated: events=%+v err=%v", events, err)
	}
}

func TestSolanaConfiguredAssetsHandleUnsupportedTemporaryTokenAccount(t *testing.T) {
	for _, fault := range []string{"none", "missing initialization mint", "missing initialization owner", "invalid initialization owner", "conflicting destination mint"} {
		t.Run(fault, func(t *testing.T) {
			transaction := solanaRecoveryTransaction(t)
			initialization := map[string]any{"account": solanaTokenProgram, "mint": solanaRecoveryTo, "owner": solanaRecoveryFrom, "rentSysvar": "SysvarRent111111111111111111111111111111111"}
			transaction.Meta.PostTokenBalances = []solanaTokenBalance{{AccountIndex: 3, Mint: solanaRecoveryTo, Owner: solanaRecoveryTo, ProgramID: solanaTokenProgram}}
			switch fault {
			case "missing initialization mint":
				delete(initialization, "mint")
			case "missing initialization owner":
				delete(initialization, "owner")
			case "invalid initialization owner":
				initialization["owner"] = "invalid-owner"
			case "conflicting destination mint":
				transaction.Meta.PostTokenBalances[0].Mint = "EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v"
			}
			parsed, err := json.Marshal(map[string]any{"type": "initializeAccount", "info": initialization})
			if err != nil {
				t.Fatal(err)
			}
			// TEMP is initialized and closed in this transaction, so only DEST
			// appears in pre/post balances. The unchecked transfer has no mint.
			transaction.Meta.InnerInstructions = []solanaInnerGroup{{Index: 1, Instructions: []solanaInstruction{
				{Program: "system", ProgramID: solanaRecoveryFrom, Parsed: json.RawMessage(`{"type":"createAccount","info":{"source":"` + solanaRecoveryFrom + `","newAccount":"` + solanaTokenProgram + `","owner":"` + solanaTokenProgram + `","lamports":2039280,"space":165}}`)},
				{Program: "spl-token", ProgramID: solanaTokenProgram, Parsed: parsed},
				{Program: "spl-token", ProgramID: solanaTokenProgram, Parsed: json.RawMessage(`{"type":"transfer","info":{"source":"` + solanaTokenProgram + `","destination":"` + solanaToken2022Program + `","amount":"42"}}`)},
				{Program: "spl-token", ProgramID: solanaTokenProgram, Parsed: json.RawMessage(`{"type":"closeAccount","info":{"account":"` + solanaTokenProgram + `","destination":"` + solanaRecoveryFrom + `","owner":"` + solanaRecoveryFrom + `"}}`)},
			}}}
			assets := map[string]SolanaAsset{
				"EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v": {AssetID: "usdc-solana", Decimals: 6},
				"Es9vMFrzaCERmJfrF4H2FYD4KCoNkY11McCe8BenwNYB": {AssetID: "usdt-solana", Decimals: 6},
			}
			events, err := solanaRecoverySource(assets).normalizeSolanaTransaction(transaction, 7, solanaRecoveryTo, time.Unix(100, 0).UTC(), 9)
			if fault == "none" {
				if err != nil {
					t.Fatalf("unsupported temporary token lifecycle blocked native payment: %v", err)
				}
				assertSolanaRecoveryNative(t, events)
				return
			}
			var providerError *ProviderError
			if !errors.As(err, &providerError) || providerError.Kind != ErrorMalformed || len(events) != 0 {
				t.Fatalf("incomplete/conflicting temporary token evidence did not fail closed: events=%+v err=%v", events, err)
			}
		})
	}
}

func TestSolanaTokenAccountEvidenceCannotBeOverwritten(t *testing.T) {
	for _, origin := range []string{"balance", "initialization"} {
		for _, conflict := range []string{"mint", "owner", "program"} {
			t.Run(origin+"/"+conflict, func(t *testing.T) {
				transaction := solanaRecoveryTransaction(t)
				metadata := solanaTokenBalance{AccountIndex: 2, Mint: solanaRecoveryTo, Owner: solanaRecoveryFrom, ProgramID: solanaTokenProgram}
				transaction.Meta.PreTokenBalances = []solanaTokenBalance{metadata}
				transaction.Meta.PostTokenBalances = []solanaTokenBalance{{AccountIndex: 3, Mint: solanaRecoveryTo, Owner: solanaRecoveryTo, ProgramID: solanaTokenProgram}}
				conflicting := metadata
				switch conflict {
				case "mint":
					conflicting.Mint = solanaRecoveryFrom
				case "owner":
					conflicting.Owner = solanaRecoveryTo
				case "program":
					conflicting.ProgramID = solanaToken2022Program
				}
				if origin == "balance" {
					// The post balance must not silently replace conflicting pre evidence.
					transaction.Meta.PreTokenBalances[0] = conflicting
					transaction.Meta.PostTokenBalances = append(transaction.Meta.PostTokenBalances, metadata)
				} else {
					parsed, err := json.Marshal(map[string]any{"type": "initializeAccount", "info": map[string]any{"account": solanaTokenProgram, "mint": conflicting.Mint, "owner": conflicting.Owner}})
					if err != nil {
						t.Fatal(err)
					}
					transaction.Transaction.Message.Instructions = append(transaction.Transaction.Message.Instructions, solanaInstruction{Program: "spl-token", ProgramID: conflicting.ProgramID, Parsed: parsed})
				}
				transaction.Transaction.Message.Instructions = append(transaction.Transaction.Message.Instructions, solanaInstruction{Program: "spl-token", ProgramID: solanaTokenProgram, Parsed: json.RawMessage(`{"type":"transfer","info":{"source":"` + solanaTokenProgram + `","destination":"` + solanaToken2022Program + `","amount":"42"}}`)})
				events, err := solanaRecoverySource(map[string]SolanaAsset{solanaRecoveryTo: {AssetID: "configured-token", Decimals: 6}}).normalizeSolanaTransaction(transaction, 7, solanaRecoveryTo, time.Unix(100, 0).UTC(), 9)
				var providerError *ProviderError
				if !errors.As(err, &providerError) || providerError.Kind != ErrorMalformed || len(events) != 0 {
					t.Fatalf("conflicting account evidence was overwritten: events=%+v err=%v", events, err)
				}
			})
		}
	}
}

func TestSolanaTemporaryTokenInitializationRequiresRecognizedProgram(t *testing.T) {
	for _, program := range []string{"", solanaRecoveryFrom, solanaToken2022Program} {
		t.Run(program, func(t *testing.T) {
			transaction := solanaRecoveryTransaction(t)
			transaction.Meta.PostTokenBalances = []solanaTokenBalance{{AccountIndex: 3, Mint: solanaRecoveryTo, Owner: solanaRecoveryTo, ProgramID: solanaTokenProgram}}
			transaction.Meta.InnerInstructions = []solanaInnerGroup{{Index: 0, Instructions: []solanaInstruction{
				{Program: "spl-token", ProgramID: program, Parsed: json.RawMessage(`{"type":"initializeAccount","info":{"account":"` + solanaTokenProgram + `","mint":"` + solanaRecoveryTo + `","owner":"` + solanaRecoveryFrom + `"}}`)},
				{Program: "spl-token", ProgramID: solanaTokenProgram, Parsed: json.RawMessage(`{"type":"transfer","info":{"source":"` + solanaTokenProgram + `","destination":"` + solanaToken2022Program + `","amount":"42"}}`)},
			}}}
			events, err := solanaRecoverySource(map[string]SolanaAsset{solanaRecoveryFrom: {AssetID: "configured-token", Decimals: 6}}).normalizeSolanaTransaction(transaction, 7, solanaRecoveryTo, time.Unix(100, 0).UTC(), 9)
			var providerError *ProviderError
			if !errors.As(err, &providerError) || providerError.Kind != ErrorMalformed || len(events) != 0 {
				t.Fatalf("unrecognized or mismatched initialization program authorized token filtering: events=%+v err=%v", events, err)
			}
		})
	}
}

func TestSolanaTemporaryTokenInitializationRequiresAccountKey(t *testing.T) {
	transaction := solanaRecoveryTransaction(t)
	account := "EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v"
	transaction.Meta.PostTokenBalances = []solanaTokenBalance{{AccountIndex: 3, Mint: solanaRecoveryTo, Owner: solanaRecoveryTo, ProgramID: solanaTokenProgram}}
	transaction.Meta.InnerInstructions = []solanaInnerGroup{{Index: 0, Instructions: []solanaInstruction{
		{Program: "spl-token", ProgramID: solanaTokenProgram, Parsed: json.RawMessage(`{"type":"initializeAccount","info":{"account":"` + account + `","mint":"` + solanaRecoveryTo + `","owner":"` + solanaRecoveryFrom + `"}}`)},
		{Program: "spl-token", ProgramID: solanaTokenProgram, Parsed: json.RawMessage(`{"type":"transfer","info":{"source":"` + account + `","destination":"` + solanaToken2022Program + `","amount":"42"}}`)},
	}}}
	events, err := solanaRecoverySource(map[string]SolanaAsset{solanaRecoveryFrom: {AssetID: "configured-token", Decimals: 6}}).normalizeSolanaTransaction(transaction, 7, solanaRecoveryTo, time.Unix(100, 0).UTC(), 9)
	var providerError *ProviderError
	if !errors.As(err, &providerError) || providerError.Kind != ErrorMalformed || len(events) != 0 {
		t.Fatalf("initialization outside message account keys authorized token filtering: events=%+v err=%v", events, err)
	}
}

func solanaRecoveryRPCSource(t *testing.T, transaction solanaTransaction, safe uint64) *SolanaSource {
	t.Helper()
	transactionResult, err := json.Marshal(transaction)
	if err != nil {
		t.Fatal(err)
	}
	client := fixtureClient(t, func(request *http.Request) (int, json.RawMessage) {
		return 200, rpcResult(t, request, func(method string, params []json.RawMessage) json.RawMessage {
			// Both entry points must request finalized evidence, not confirmed data.
			if len(params) > 0 {
				var options map[string]any
				if json.Unmarshal(params[len(params)-1], &options) == nil && options["commitment"] != "finalized" {
					t.Fatalf("%s did not request finalized evidence: %v", method, options)
				}
			}
			switch method {
			case "getSlot":
				result, _ := json.Marshal(safe)
				return result
			case "getBlocks":
				return json.RawMessage(`[7]`)
			case "getSignaturesForAddress":
				return json.RawMessage(`[{"signature":"` + solanaRecoverySignature + `","slot":7,"err":null,"blockTime":100},{"signature":"` + solanaRecoverySignature + `","slot":7,"err":null,"blockTime":100}]`)
			case "getBlock":
				return json.RawMessage(`{"blockhash":"` + solanaRecoveryTo + `","previousBlockhash":"` + solanaRecoveryFrom + `","parentSlot":6,"blockTime":100}`)
			case "getTransaction":
				return transactionResult
			default:
				t.Fatalf("unexpected synthetic Solana method %s", method)
				return nil
			}
		})
	})
	source, err := NewSolanaSource(SolanaConfig{HTTP: HTTPConfig{Endpoint: "https://solana-recovery.invalid", Client: client}, ProviderID: "synthetic-recovery", ChainID: "solana:mainnet", NativeAssetID: "sol", NativeDecimals: 9, WatchedAddresses: []string{solanaRecoveryTo}})
	if err != nil {
		t.Fatal(err)
	}
	return source
}

func TestSolanaNativeIndexedScanAndLookupAgreeOnCanonicalExactMoney(t *testing.T) {
	for _, malformedBalances := range []bool{false, true} {
		name := "unrelated token instructions"
		if malformedBalances {
			name = "unrelated malformed balances"
		}
		t.Run(name, func(t *testing.T) {
			transaction := solanaRecoveryTransaction(t)
			transaction.Transaction.Message.Instructions = append(transaction.Transaction.Message.Instructions, solanaInstruction{Program: "spl-token", ProgramID: solanaTokenProgram, Parsed: json.RawMessage(`{"type":"transfer","info":{}}`)})
			if malformedBalances {
				transaction.Meta.PostTokenBalances = []solanaTokenBalance{{AccountIndex: 2, Mint: solanaRecoveryTo, ProgramID: solanaTokenProgram}}
			}
			source := solanaRecoveryRPCSource(t, transaction, 9)
			batch, err := source.ScanRange(context.Background(), 7, 7)
			if err != nil {
				t.Fatalf("indexed scan rejected native payment: %v", err)
			}
			if !batch.IndexedCheckpoint || batch.From != 7 || batch.To != 7 || len(batch.Blocks) != 1 {
				t.Fatalf("indexed checkpoint changed: %+v", batch)
			}
			assertSolanaRecoveryNative(t, batch.Events)
			for replay := 0; replay < 2; replay++ {
				events, err := source.LookupTransaction(context.Background(), "solana:mainnet", solanaRecoverySignature)
				if err != nil {
					t.Fatalf("lookup rejected native payment: %v", err)
				}
				assertSolanaRecoveryNative(t, events)
				// Storage event IDs are generated independently; the canonical tuple is the replay key.
				if events[0].Identity != batch.Events[0].Identity || !reflect.DeepEqual(events[0].Amount, batch.Events[0].Amount) || events[0].EvidenceHash != batch.Events[0].EvidenceHash {
					t.Fatalf("scan/lookup replay binding differs: scan=%+v lookup=%+v", batch.Events[0], events[0])
				}
			}
		})
	}
}

func TestSolanaNativeLookupRejectsInvalidFinalityBinding(t *testing.T) {
	for _, fault := range []string{"unfinalized", "wrong signature", "wrong block time", "failed execution"} {
		t.Run(fault, func(t *testing.T) {
			transaction := solanaRecoveryTransaction(t)
			safe := uint64(9)
			switch fault {
			case "unfinalized":
				safe = 6
			case "wrong signature":
				transaction.Transaction.Signatures[0] = "2222222222222222222222222222222222222222222222222222222222222222"
			case "wrong block time":
				wrongTime := int64(101)
				transaction.BlockTime = &wrongTime
			case "failed execution":
				transaction.Meta.Err = json.RawMessage(`{"InstructionError":[0,"Custom"]}`)
			}
			events, err := solanaRecoveryRPCSource(t, transaction, safe).LookupTransaction(context.Background(), "solana:mainnet", solanaRecoverySignature)
			if len(events) != 0 || (fault != "failed execution" && err == nil) || (fault == "failed execution" && err != nil) {
				t.Fatalf("invalid finality/execution binding emitted a payment: events=%+v err=%v", events, err)
			}
		})
	}
}

func TestSolanaNativeLookupIgnoresUnrelatedMalformedTokenBalances(t *testing.T) {
	transaction := solanaRecoveryTransaction(t)
	transaction.Meta.PreTokenBalances = []solanaTokenBalance{{AccountIndex: 2, Mint: solanaRecoveryTo, ProgramID: solanaTokenProgram}}
	events, err := solanaRecoveryRPCSource(t, transaction, 9).LookupTransaction(context.Background(), "solana:mainnet", solanaRecoverySignature)
	if err != nil {
		t.Fatalf("lookup rejected native payment due to unrelated token metadata: %v", err)
	}
	assertSolanaRecoveryNative(t, events)
}
