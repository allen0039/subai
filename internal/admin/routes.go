package admin

import (
	"net/http"
	"regexp"
	"strings"
)

var resourceUUID = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
var requestID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,200}$`)

// Routes registers the /api/admin tree (§17.2) plus the public OAuth callback.
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()

	// Public: login only. The OAuth callback is mounted on the OUTER mux by
	// internal/server so it is reachable in production too (review P1-6).
	mux.HandleFunc("/api/admin/session", s.handleSession)
	mux.Handle("/api/admin/me/password", s.RequireSession(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			s.writeErr(w, 405, "method not allowed")
			return
		}
		s.changeOwnPassword(w, r)
	}))
	mux.Handle("/api/admin/me/subscriptions", s.RequireSession(getOnly(func(w http.ResponseWriter, r *http.Request) {
		s.listSubscriptions(w, r, identityFrom(r).MemberID)
	})))
	mux.Handle("/api/admin/me/keys", s.RequireSession(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			s.listOwnKeys(w, r)
		case http.MethodPost:
			s.createOwnKey(w, r)
		default:
			s.writeErr(w, 405, "method not allowed")
		}
	}))
	mux.Handle("/api/admin/me/keys/", s.RequireSession(func(w http.ResponseWriter, r *http.Request) {
		rest := pathID(r, "/api/admin/me/keys/")
		id := strings.TrimSuffix(rest, "/revoke")
		if !resourceUUID.MatchString(id) {
			s.writeErr(w, 400, "invalid key ID")
			return
		}
		s.ownKey(w, r, id)
	}))

	// Authenticated management surface.
	mux.Handle("/api/admin/members", s.RequireAdmin(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			s.listMembers(w, r)
		case http.MethodPost:
			s.createMember(w, r)
		default:
			s.writeErr(w, 405, "method not allowed")
		}
	}))
	mux.Handle("/api/admin/members/", s.RequireAdmin(func(w http.ResponseWriter, r *http.Request) {
		id := pathID(r, "/api/admin/members/")
		if r.Method != http.MethodPatch {
			s.writeErr(w, 405, "method not allowed")
			return
		}
		if !resourceUUID.MatchString(id) {
			s.writeErr(w, 400, "invalid member ID")
			return
		}
		s.patchMember(w, r, id)
	}))
	// "users" is the new product vocabulary. The legacy members endpoints
	// remain available to existing automations during the migration.
	mux.Handle("/api/admin/users", s.RequireAdmin(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			s.listMembers(w, r)
		case http.MethodPost:
			s.createMember(w, r)
		default:
			s.writeErr(w, 405, "method not allowed")
		}
	}))
	mux.Handle("/api/admin/users/", s.RequireAdmin(func(w http.ResponseWriter, r *http.Request) {
		rest := pathID(r, "/api/admin/users/")
		parts := strings.Split(rest, "/")
		if len(parts) < 1 || !resourceUUID.MatchString(parts[0]) {
			s.writeErr(w, 400, "invalid user ID")
			return
		}
		if len(parts) == 1 {
			if r.Method != http.MethodPatch {
				s.writeErr(w, 405, "method not allowed")
				return
			}
			s.patchMember(w, r, parts[0])
			return
		}
		if len(parts) == 2 && parts[1] == "subscriptions" {
			if r.Method != http.MethodGet {
				s.writeErr(w, 405, "method not allowed")
				return
			}
			s.listSubscriptions(w, r, parts[0])
			return
		}
		if len(parts) == 2 && (parts[1] == "account-grants" || parts[1] == "pool-grants") {
			kind := "account"
			if parts[1] == "pool-grants" {
				kind = "pool"
			}
			switch r.Method {
			case http.MethodGet:
				s.listUserGrants(w, r, parts[0], kind)
			case http.MethodPost:
				s.createUserGrant(w, r, parts[0], kind)
			default:
				s.writeErr(w, 405, "method not allowed")
			}
			return
		}
		if len(parts) == 3 && (parts[1] == "account-grants" || parts[1] == "pool-grants") && resourceUUID.MatchString(parts[2]) {
			if r.Method != http.MethodPatch {
				s.writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
				return
			}
			kind := "account"
			if parts[1] == "pool-grants" {
				kind = "pool"
			}
			s.patchUserGrant(w, r, parts[0], kind, parts[2])
			return
		}
		s.writeErr(w, 404, "unknown user action")
	}))

	mux.Handle("/api/admin/account-pools", s.RequireAdmin(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			s.listGroups(w, r)
		case http.MethodPost:
			s.createGroup(w, r)
		default:
			s.writeErr(w, 405, "method not allowed")
		}
	}))
	mux.Handle("/api/admin/account-pools/", s.RequireAdmin(func(w http.ResponseWriter, r *http.Request) {
		rest := pathID(r, "/api/admin/account-pools/")
		parts := strings.Split(rest, "/")
		if len(parts) < 1 || !resourceUUID.MatchString(parts[0]) {
			s.writeErr(w, 400, "invalid account pool ID")
			return
		}
		switch {
		case len(parts) == 1 && r.Method == http.MethodDelete:
			s.deleteGroup(w, r, parts[0])
		case len(parts) == 2 && parts[1] == "accounts" && r.Method == http.MethodPost:
			s.groupAddAccount(w, r, parts[0])
		case len(parts) == 3 && parts[1] == "accounts" && r.Method == http.MethodDelete && resourceUUID.MatchString(parts[2]):
			s.groupRemoveAccount(w, r, parts[0], parts[2])
		default:
			s.writeErr(w, 405, "method not allowed")
		}
	}))

	mux.Handle("/api/admin/plans", s.RequireAdmin(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			s.listPlans(w, r)
		case http.MethodPost:
			s.createPlan(w, r)
		default:
			s.writeErr(w, 405, "method not allowed")
		}
	}))
	mux.Handle("/api/admin/plans/", s.RequireAdmin(func(w http.ResponseWriter, r *http.Request) {
		rest := pathID(r, "/api/admin/plans/")
		parts := strings.Split(rest, "/")
		if len(parts) < 1 || !resourceUUID.MatchString(parts[0]) {
			s.writeErr(w, 400, "invalid plan ID")
			return
		}
		switch {
		case len(parts) == 2 && parts[1] == "publish" && r.Method == http.MethodPost:
			s.publishPlan(w, r, parts[0])
		case len(parts) == 2 && parts[1] == "versions" && r.Method == http.MethodPost:
			s.createPlanVersion(w, r, parts[0])
		default:
			s.writeErr(w, 405, "method not allowed")
		}
	}))
	mux.Handle("/api/admin/subscriptions", s.RequireAdmin(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			s.listSubscriptions(w, r, "")
		case http.MethodPost:
			s.createSubscription(w, r)
		default:
			s.writeErr(w, 405, "method not allowed")
		}
	}))
	mux.Handle("/api/admin/subscriptions/", s.RequireAdmin(func(w http.ResponseWriter, r *http.Request) {
		id := pathID(r, "/api/admin/subscriptions/")
		if !resourceUUID.MatchString(id) {
			s.writeErr(w, 400, "invalid subscription ID")
			return
		}
		if r.Method != http.MethodPatch {
			s.writeErr(w, 405, "method not allowed")
			return
		}
		s.patchSubscription(w, r, id)
	}))

	mux.Handle("/api/admin/clients", s.RequireAdmin(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			s.listClients(w, r)
		case http.MethodPost:
			s.createClient(w, r)
		default:
			s.writeErr(w, 405, "method not allowed")
		}
	}))
	mux.Handle("/api/admin/clients/", s.RequireAdmin(func(w http.ResponseWriter, r *http.Request) {
		id := pathID(r, "/api/admin/clients/")
		if r.Method != http.MethodPatch {
			s.writeErr(w, 405, "method not allowed")
			return
		}
		if !resourceUUID.MatchString(id) {
			s.writeErr(w, 400, "invalid client ID")
			return
		}
		s.patchClient(w, r, id)
	}))

	mux.Handle("/api/admin/keys", s.RequireAdmin(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			s.listKeys(w, r)
		case http.MethodPost:
			s.createKey(w, r)
		default:
			s.writeErr(w, 405, "method not allowed")
		}
	}))
	mux.Handle("/api/admin/keys/", s.RequireAdmin(func(w http.ResponseWriter, r *http.Request) {
		id := pathID(r, "/api/admin/keys/")
		if strings.HasSuffix(id, "/revoke") {
			base := strings.TrimSuffix(id, "/revoke")
			if r.Method != http.MethodPost {
				s.writeErr(w, 405, "method not allowed")
				return
			}
			if !resourceUUID.MatchString(base) {
				s.writeErr(w, 400, "invalid key ID")
				return
			}
			s.revokeKey(w, r, base)
			return
		}
		if strings.HasSuffix(id, "/routes") {
			base := strings.TrimSuffix(id, "/routes")
			if r.Method != http.MethodGet && r.Method != http.MethodPost && r.Method != http.MethodDelete {
				s.writeErr(w, 405, "method not allowed")
				return
			}
			if !resourceUUID.MatchString(base) {
				s.writeErr(w, 400, "invalid key ID")
				return
			}
			s.keyRoutes(w, r, base)
			return
		}
		if !resourceUUID.MatchString(id) {
			s.writeErr(w, 400, "invalid key ID")
			return
		}
		if r.Method != http.MethodPatch {
			s.writeErr(w, 405, "method not allowed")
			return
		}
		s.patchKey(w, r, id)
	}))

	mux.Handle("/api/admin/proxies", s.RequireAdmin(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			s.listProxies(w, r)
		case http.MethodPost:
			s.createProxy(w, r)
		default:
			s.writeErr(w, 405, "method not allowed")
		}
	}))
	mux.Handle("/api/admin/proxies/", s.RequireAdmin(func(w http.ResponseWriter, r *http.Request) {
		id := pathID(r, "/api/admin/proxies/")
		if strings.HasSuffix(r.URL.Path, "/test") {
			base := strings.TrimSuffix(id, "/test")
			if r.Method != http.MethodPost {
				s.writeErr(w, 405, "method not allowed")
				return
			}
			if !resourceUUID.MatchString(base) {
				s.writeErr(w, 400, "invalid proxy ID")
				return
			}
			s.testProxy(w, r, base)
			return
		}
		if !resourceUUID.MatchString(id) {
			s.writeErr(w, 400, "invalid proxy ID")
			return
		}
		switch r.Method {
		case http.MethodPatch:
			s.patchProxy(w, r, id)
		case http.MethodDelete:
			s.deleteProxy(w, r, id)
		default:
			s.writeErr(w, 405, "method not allowed")
		}
	}))

	mux.Handle("/api/admin/egress-policies", s.RequireAdmin(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			s.listEgressPolicies(w, r)
		case http.MethodPost:
			s.createEgressPolicy(w, r)
		default:
			s.writeErr(w, 405, "method not allowed")
		}
	}))

	mux.Handle("/api/admin/accounts", s.RequireAdmin(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			s.listAccounts(w, r)
		case http.MethodPost:
			s.createAccount(w, r)
		default:
			s.writeErr(w, 405, "method not allowed")
		}
	}))
	mux.Handle("/api/admin/accounts/", s.RequireAdmin(func(w http.ResponseWriter, r *http.Request) {
		id := pathID(r, "/api/admin/accounts/")
		if strings.HasSuffix(id, "/holds") {
			base := strings.TrimSuffix(id, "/holds")
			if !resourceUUID.MatchString(base) {
				s.writeErr(w, 400, "invalid account ID")
				return
			}
			s.accountHolds(w, r, base)
			return
		}
		if !resourceUUID.MatchString(id) {
			s.writeErr(w, 400, "invalid account ID")
			return
		}
		if r.Method != http.MethodPatch {
			s.writeErr(w, 405, "method not allowed")
			return
		}
		s.patchAccount(w, r, id)
	}))

	mux.Handle("/api/admin/groups", s.RequireAdmin(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			s.listGroups(w, r)
		case http.MethodPost:
			s.createGroup(w, r)
		default:
			s.writeErr(w, 405, "method not allowed")
		}
	}))
	mux.Handle("/api/admin/groups/", s.RequireAdmin(func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, "/api/admin/groups/")
		parts := strings.Split(rest, "/")
		switch {
		case len(parts) == 1 && r.Method == http.MethodDelete:
			if !resourceUUID.MatchString(parts[0]) {
				s.writeErr(w, 400, "invalid group ID")
				return
			}
			s.deleteGroup(w, r, parts[0])
		case len(parts) == 1 && r.Method == http.MethodPost:
			if !resourceUUID.MatchString(parts[0]) {
				s.writeErr(w, 400, "invalid group ID")
				return
			}
			s.groupAddAccount(w, r, parts[0])
		case len(parts) == 3 && parts[1] == "accounts" && r.Method == http.MethodDelete:
			if !resourceUUID.MatchString(parts[0]) {
				s.writeErr(w, 400, "invalid group ID")
				return
			}
			if !resourceUUID.MatchString(parts[2]) {
				s.writeErr(w, 400, "invalid account ID")
				return
			}
			s.groupRemoveAccount(w, r, parts[0], parts[2])
		default:
			s.writeErr(w, 405, "method not allowed")
		}
	}))

	mux.Handle("/api/admin/budget-policies", s.RequireAdmin(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			s.listBudgetPolicies(w, r)
		case http.MethodPost:
			s.createBudgetPolicy(w, r)
		default:
			s.writeErr(w, 405, "method not allowed")
		}
	}))
	mux.Handle("/api/admin/budget-periods", s.RequireAdmin(getOnly(s.listBudgetPeriods)))
	mux.Handle("/api/admin/budget-policies/", s.RequireAdmin(func(w http.ResponseWriter, r *http.Request) {
		id := pathID(r, "/api/admin/budget-policies/")
		if r.Method != http.MethodPatch {
			s.writeErr(w, 405, "method not allowed")
			return
		}
		if !resourceUUID.MatchString(id) {
			s.writeErr(w, 400, "invalid policy ID")
			return
		}
		s.patchBudgetPolicy(w, r, id)
	}))
	mux.Handle("/api/admin/ledger", s.RequireAdmin(getOnly(s.listLedger)))

	mux.Handle("/api/admin/prices/versions", s.RequireAdmin(getOnly(s.listPriceVersions)))
	mux.Handle("/api/admin/prices/sync", s.RequireAdmin(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			s.writeErr(w, 405, "method not allowed")
			return
		}
		s.syncPrices(w, r)
	}))
	mux.Handle("/api/admin/prices/overrides", s.RequireAdmin(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			s.writeErr(w, 405, "method not allowed")
			return
		}
		s.overridePrice(w, r)
	}))

	mux.Handle("/api/admin/audit/rules", s.RequireAdmin(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			s.listAuditRules(w, r)
		case http.MethodPost:
			s.validateAuditRule(w, r)
		default:
			s.writeErr(w, 405, "method not allowed")
		}
	}))
	mux.Handle("/api/admin/audit/rules/", s.RequireAdmin(func(w http.ResponseWriter, r *http.Request) {
		id := pathID(r, "/api/admin/audit/rules/")
		if strings.HasSuffix(r.URL.Path, "/validate") {
			if r.Method != http.MethodPost {
				s.writeErr(w, 405, "method not allowed")
				return
			}
			s.validateAuditRule(w, r)
			return
		}
		if !resourceUUID.MatchString(id) {
			s.writeErr(w, 400, "invalid rule ID")
			return
		}
		if r.Method != http.MethodPatch {
			s.writeErr(w, 405, "method not allowed")
			return
		}
		s.patchAuditRule(w, r, id)
	}))
	mux.Handle("/api/admin/audit/events", s.RequireAdmin(getOnly(s.listAuditEvents)))
	mux.Handle("/api/admin/audit/events/", s.RequireAdmin(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(pathID(r, "/api/admin/audit/events/"), "/")
		if len(parts) != 2 || parts[0] == "" || parts[1] != "review" {
			s.writeErr(w, 404, "unknown audit event action")
			return
		}
		if r.Method != http.MethodPost {
			s.writeErr(w, 405, "method not allowed")
			return
		}
		if !resourceUUID.MatchString(parts[0]) {
			s.writeErr(w, 400, "invalid event ID")
			return
		}
		s.reviewAuditEvent(w, r, parts[0])
	}))

	mux.Handle("/api/admin/accounts/oauth/sessions", s.RequireAdmin(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			s.writeErr(w, 405, "method not allowed")
			return
		}
		s.startOAuthSession(w, r)
	}))
	mux.Handle("/api/admin/accounts/oauth/sessions/", s.RequireAdmin(func(w http.ResponseWriter, r *http.Request) {
		rest := pathID(r, "/api/admin/accounts/oauth/sessions/")
		parts := strings.Split(rest, "/")
		if len(parts) < 1 || !resourceUUID.MatchString(parts[0]) {
			s.writeErr(w, 400, "invalid session ID")
			return
		}
		if len(parts) == 1 && r.Method == http.MethodGet {
			s.getOAuthSession(w, r, parts[0])
			return
		}
		if len(parts) == 2 && parts[1] == "callback" && r.Method == http.MethodPost {
			s.completeOAuthCallbackURL(w, r, parts[0])
			return
		}
		s.writeErr(w, 405, "method not allowed")
	}))

	mux.Handle("/api/admin/requests/", s.RequireAdmin(func(w http.ResponseWriter, r *http.Request) {
		id := pathID(r, "/api/admin/requests/")
		if strings.HasSuffix(r.URL.Path, "/resolve-unknown") {
			base := strings.TrimSuffix(id, "/resolve-unknown")
			if r.Method != http.MethodPost {
				s.writeErr(w, 405, "method not allowed")
				return
			}
			if !requestID.MatchString(base) {
				s.writeErr(w, 400, "invalid request ID")
				return
			}
			s.resolveUnknown(w, r, base)
			return
		}
		s.writeErr(w, 404, "unknown request action")
	}))
	mux.Handle("/api/admin/status", s.RequireAdmin(getOnly(s.status)))
	mux.Handle("/api/admin/admin-events", s.RequireAdmin(getOnly(s.listAdminEvents)))

	return mux
}

func getOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		next(w, r)
	}
}

// pathID extracts the trailing path segment(s) after prefix.
func pathID(r *http.Request, prefix string) string {
	p := strings.TrimPrefix(r.URL.Path, prefix)
	if i := strings.Index(p, "/"); i >= 0 {
		// keep sub-action paths joined; caller splits
		return p
	}
	return p
}
