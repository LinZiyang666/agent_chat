package v1

import (
	"context"
	"net/http"
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
		actor, ok := auth.AccountFromContext(r.Context())
		if !ok {
			WriteError(w, errcode.New(errcode.Internal, "no actor in context"))
			return
		}

		// Discord is the authority on bot identity. Probe the token
		// first so we learn the bot's snowflake + username before any
		// local writes; if the local account.Name diverges from the
		// Discord-side username we'll snap the local name to match
		// inside the write tx below.
		//
		// The real Discord prober ignores the hint (GET /users/@me is
		// fully self-describing); the mock prober used by tests echoes
		// hint.Username as the default when no override is staged, so
		// we still pass account.Name through to keep test fixtures
		// well-defined.
		//
		// Test rigs without a prober skip identity-sync entirely (just
		// persist the encrypted token); production deps always inject
		// one — see cmd/agentchatd/cmds/serve.go.
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
		var (
			probed      bot.Identity
			usingProber = prober != nil
		)
		if usingProber {
			id1, perr := prober.Probe(r.Context(), req.BotToken, bot.Identity{Username: accountName})
			if perr != nil {
				WriteError(w, perr)
				return
			}
			probed = id1
		}

		enc, err := crypto.AESGCMEncrypt(masterKey, []byte(req.BotToken))
		if err != nil {
			WriteError(w, err)
			return
		}
		var (
			updated    *store.Account
			nameSynced bool
			oldName    string
		)
		if err := bundler.WithTx(r.Context(), func(b store.Bundle) error {
			a, err := b.Accounts.Get(r.Context(), id)
			if err != nil {
				return err
			}
			a.BotTokenEnc = enc
			a.UpdatedAt = time.Now().UTC()
			if usingProber && probed.UserID != "" {
				a.BotUserID = probed.UserID
			}
			// Snap local name to whatever Discord says. If the new
			// name collides with another account, Accounts.Update
			// returns CONFLICT (the unique-name guard).
			if usingProber && probed.Username != "" && probed.Username != a.Name {
				oldName = a.Name
				a.Name = probed.Username
				nameSynced = true
			}
			if err := b.Accounts.Update(r.Context(), a); err != nil {
				return err
			}
			updated = a
			payload := map[string]any{}
			if usingProber {
				payload["bot_user_id"] = probed.UserID
				payload["bot_username"] = probed.Username
				if nameSynced {
					payload["renamed_local_from"] = oldName
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
