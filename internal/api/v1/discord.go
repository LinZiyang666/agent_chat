package v1

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/LinZiyang666/agentchat/internal/account"
	"github.com/LinZiyang666/agentchat/internal/audit"
	"github.com/LinZiyang666/agentchat/internal/auth"
	"github.com/LinZiyang666/agentchat/internal/bot"
	"github.com/LinZiyang666/agentchat/internal/connector"
	"github.com/LinZiyang666/agentchat/internal/crypto"
	"github.com/LinZiyang666/agentchat/internal/errcode"
	"github.com/LinZiyang666/agentchat/internal/message"
	"github.com/LinZiyang666/agentchat/internal/store"
)

// SetDiscordRequest is the body of POST /v1/accounts/{id}/discord.
type SetDiscordRequest struct {
	BotToken string `json:"bot_token"`
	// ForceRename, when true, asks the daemon to rename the Discord
	// bot user to the agentchat account.Name if the two don't match
	// (M9 Phase 2). Without this flag, a mismatch returns CONFLICT.
	// Discord enforces a per-bot username rate limit (2/h); the
	// daemon surfaces UNAVAILABLE if the rename is rejected.
	ForceRename bool `json:"force_rename,omitempty"`
}

// StatusResponse is the body of GET /v1/accounts/{id}/status.
type StatusResponse struct {
	Account        AccountResponse `json:"account"`
	HasBotToken    bool            `json:"has_bot_token"`
	ProviderStatus string          `json:"provider_status"`
	Identity       *bot.Identity   `json:"identity,omitempty"`
}

// SetDiscord handles POST /v1/accounts/{id}/discord. The body's
// bot_token is AES-GCM encrypted with the daemon's master key and
// stored on the account. The plaintext never touches the disk after
// this handler returns.
//
// Transactional: the encrypted-token update and the audit row commit
// together via bundler.WithTx.
//
// M8-Q-P1-010: dropped the unused `svc *account.Service` parameter.
// The handler resolves the target account directly through the
// transaction's b.Accounts repo, so the service handle was dead.
func SetDiscord(bundler store.Bundler, recorder *audit.Recorder, masterKey []byte, prober bot.IdentityProber) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		var req SetDiscordRequest
		if err := DecodeJSON(r, &req); err != nil {
			WriteError(w, err)
			return
		}
		if req.BotToken == "" {
			WriteError(w, errcode.New(errcode.InvalidArgument, "bot_token is empty"))
			return
		}
		// M9 Phase 2 design (docs/06-cli-redesign.md §6.2): the
		// `force_rename` toggle is reachable via either the JSON
		// body field OR a `?force_rename=true` query string. Accept
		// both — body wins when both are set, but a true on either
		// channel enables the rename branch.
		if q := r.URL.Query().Get("force_rename"); q != "" {
			if v, err := strconv.ParseBool(q); err != nil {
				WriteError(w, errcode.New(errcode.InvalidArgument,
					"force_rename must be a boolean (true|false|1|0); got %q", q))
				return
			} else if v {
				req.ForceRename = true
			}
		}
		actor, ok := auth.AccountFromContext(r.Context())
		if !ok {
			WriteError(w, errcode.New(errcode.Internal, "no actor in context"))
			return
		}

		// Fetch the target account up-front (outside the write tx) so
		// the prober knows what account.Name to match against. Failing
		// here surfaces NotFound to the admin before we go bother
		// Discord.
		var accountName string
		if err := bundler.WithTx(r.Context(), func(b store.Bundle) error {
			a, err := b.Accounts.Get(r.Context(), id)
			if err != nil {
				return err
			}
			accountName = a.Name
			return nil
		}); err != nil {
			WriteError(w, err)
			return
		}

		// M9 Phase 2: probe the token against the platform BEFORE
		// persisting. This pins down two invariants the outbound
		// @<name> parser depends on:
		//   1. account.Name == bot.Username (so `@alice` in content
		//      reliably refers to the Discord-side identity alice).
		//   2. accounts.bot_user_id is populated, so the ingester /
		//      send path can map between snowflake and account_id
		//      without an extra round-trip through OnlineAccount.
		// If the daemon has no prober wired in (mock test rigs that
		// haven't migrated yet), fall back to the legacy behaviour:
		// just persist the encrypted token. Production deps always
		// inject one — see cmd/agentchatd/cmds/serve.go.
		var (
			probed         bot.Identity
			usingProber    = prober != nil
			didForceRename bool
		)
		if usingProber {
			id1, perr := prober.Probe(r.Context(), req.BotToken, bot.Identity{Username: accountName})
			if perr != nil {
				WriteError(w, perr)
				return
			}
			probed = id1
			if probed.Username != accountName {
				if !req.ForceRename {
					WriteError(w, errcode.New(errcode.Conflict,
						"bot username %q does not match account name %q; rename the bot on the Discord developer portal or retry with force_rename=true",
						probed.Username, accountName))
					return
				}
				if rerr := prober.Rename(r.Context(), req.BotToken, accountName); rerr != nil {
					WriteError(w, rerr)
					return
				}
				didForceRename = true
				probed.Username = accountName
			}
		}

		enc, err := crypto.AESGCMEncrypt(masterKey, []byte(req.BotToken))
		if err != nil {
			WriteError(w, err)
			return
		}
		var updated *store.Account
		if err := bundler.WithTx(r.Context(), func(b store.Bundle) error {
			a, err := b.Accounts.Get(r.Context(), id)
			if err != nil {
				return err
			}
			a.BotTokenEnc = enc
			a.UpdatedAt = time.Now().UTC()
			// Persist the bot_user_id so the ingester's mention
			// resolver and `room invite` don't have to wait for the
			// first OnlineAccount call to learn it (M9 Phase 2).
			if usingProber && probed.UserID != "" {
				a.BotUserID = probed.UserID
			}
			if err := b.Accounts.Update(r.Context(), a); err != nil {
				return err
			}
			updated = a
			payload := map[string]any{}
			if usingProber {
				payload["bot_user_id"] = probed.UserID
				payload["bot_username"] = probed.Username
				if didForceRename {
					payload["force_rename"] = true
				}
			}
			if len(payload) == 0 {
				payload = nil
			}
			return recorder.RecordVia(r.Context(), b.Audit, actor.ID,
				audit.ActionAccountSetDiscord, id, payload)
		}); err != nil {
			WriteError(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, AccountToResponse(updated))
	}
}

// OnlineAccount handles POST /v1/accounts/{id}/online.
//
// Sequence:
//  1. Read the account; reject if no bot token configured.
//  2. Decrypt the bot token.
//  3. Tell the connector to build + Connect a Provider for this
//     account. Slow (network handshake) — runs outside any tx.
//  4. In a transaction, advance the account's lifecycle to "online"
//     and write the account.online audit row.
//
// If step 4 fails the just-connected provider is best-effort
// disconnected so the in-memory and on-disk states do not diverge.
func OnlineAccount(svc *account.Service, conn *connector.Connector,
	bundler store.Bundler, recorder *audit.Recorder, masterKey []byte,
	ingester *message.Ingester,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		actor, ok := auth.AccountFromContext(r.Context())
		if !ok {
			WriteError(w, errcode.New(errcode.Internal, "no actor in context"))
			return
		}
		a, err := svc.Get(r.Context(), id)
		if err != nil {
			WriteError(w, err)
			return
		}
		if len(a.BotTokenEnc) == 0 {
			WriteError(w, errcode.New(errcode.InvalidArgument,
				"account %s has no Discord bot token set", id))
			return
		}
		plain, err := crypto.AESGCMDecrypt(masterKey, a.BotTokenEnc)
		if err != nil {
			WriteError(w, err)
			return
		}
		if err := conn.Connect(r.Context(), id, string(plain), bot.Identity{Username: a.Name}); err != nil {
			WriteError(w, err)
			return
		}
		// Capture the Discord-side identity for the inbound-message
		// ingester's author resolution (M4). Identity() is valid now
		// that Connect has returned.
		var capturedUID string
		if p, ok := conn.Provider(id); ok {
			capturedUID = p.Identity().UserID
		}
		if err := bundler.WithTx(r.Context(), func(b store.Bundle) error {
			a2, err := b.Accounts.Get(r.Context(), id)
			if err != nil {
				return err
			}
			a2.LifecycleState = store.LifecycleOnline
			a2.UpdatedAt = time.Now().UTC()
			if capturedUID != "" {
				a2.BotUserID = capturedUID
			}
			if err := b.Accounts.Update(r.Context(), a2); err != nil {
				return err
			}
			return recorder.RecordVia(r.Context(), b.Audit, actor.ID,
				audit.ActionAccountOnline, id, nil)
		}); err != nil {
			// Roll back the in-memory provider so the two stores agree.
			_ = conn.Disconnect(context.Background(), id)
			WriteError(w, err)
			return
		}
		if ingester != nil {
			ingester.AttachAccount(id)
		}
		writeStatusResponse(w, svc, conn, r.Context(), id)
	}
}

// OfflineAccount handles POST /v1/accounts/{id}/offline.
//
// Sequence (M3-P3-002 fix): the DB lifecycle update and audit insert
// commit FIRST, then the in-memory Provider is disconnected. If the
// transaction fails the Provider stays online and the account
// lifecycle stays "online" — caller sees 500 and can retry. The
// previous order (disconnect first, then tx) could leave a split
// state where DB said online but the Provider was already gone.
//
// Pre-flight: the account must currently be online. We check via the
// Connector so we don't waste a transaction when the account is not
// online to begin with.
func OfflineAccount(svc *account.Service, conn *connector.Connector,
	bundler store.Bundler, recorder *audit.Recorder,
	ingester *message.Ingester,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		actor, ok := auth.AccountFromContext(r.Context())
		if !ok {
			WriteError(w, errcode.New(errcode.Internal, "no actor in context"))
			return
		}
		if _, online := conn.Provider(id); !online {
			WriteError(w, errcode.New(errcode.Conflict,
				"account %s has no live provider", id))
			return
		}
		if err := bundler.WithTx(r.Context(), func(b store.Bundle) error {
			a, err := b.Accounts.Get(r.Context(), id)
			if err != nil {
				return err
			}
			a.LifecycleState = store.LifecycleOffline
			a.UpdatedAt = time.Now().UTC()
			if err := b.Accounts.Update(r.Context(), a); err != nil {
				return err
			}
			return recorder.RecordVia(r.Context(), b.Audit, actor.ID,
				audit.ActionAccountOffline, id, nil)
		}); err != nil {
			WriteError(w, err)
			return
		}
		// Tx committed; detach the ingester (drops its event
		// subscription) before tearing down the in-memory Provider.
		// Disconnect would also close the subscription channel via the
		// pump tail, but explicit Detach frees the slot promptly and
		// is symmetric with AttachAccount.
		if ingester != nil {
			ingester.DetachAccount(id)
		}
		// If Disconnect itself fails the DB already says "offline", so
		// the next online attempt will recover. We surface the
		// disconnect error to the caller but the DB state is already
		// consistent with the requested offline intent.
		if err := conn.Disconnect(r.Context(), id); err != nil {
			WriteError(w, err)
			return
		}
		writeStatusResponse(w, svc, conn, r.Context(), id)
	}
}

// AccountStatus handles GET /v1/accounts/{id}/status.
func AccountStatus(svc *account.Service, conn *connector.Connector) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		writeStatusResponse(w, svc, conn, r.Context(), id)
	}
}

func writeStatusResponse(w http.ResponseWriter, svc *account.Service, conn *connector.Connector, ctx context.Context, id string) {
	a, err := svc.Get(ctx, id)
	if err != nil {
		WriteError(w, err)
		return
	}
	resp := StatusResponse{
		Account:        AccountToResponse(a),
		HasBotToken:    len(a.BotTokenEnc) > 0,
		ProviderStatus: string(conn.Status(id)),
	}
	if p, ok := conn.Provider(id); ok {
		ident := p.Identity()
		resp.Identity = &ident
	}
	WriteJSON(w, http.StatusOK, resp)
}
