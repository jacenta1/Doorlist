//! Doorlist escrow contract.
//!
//! Guests RSVP with a USDC deposit (`ft_transfer_call`). The deposit goes
//! straight on to the event's Sputnik DAO; this contract only keeps the
//! ledger. Door phones registered by the host sign each check-in. On close,
//! everyone who came is owed their deposit plus an equal share of the
//! no-shows' deposits, and `payout_batch` files one DAO Transfer proposal per
//! guest and approves it with the contract's own `doorlist` DAO role.
//!
//! This is the same state machine as `contract/core` (Go), which the web
//! app runs in demo mode. Method names, argument names and JSON field names
//! match it one to one; `testdata/split-vectors.json` is checked by both.

use near_sdk::json_types::{U128, U64};
use near_sdk::serde::{Deserialize, Serialize};
use near_sdk::serde_json::{self, json};
use near_sdk::store::LookupMap;
use near_sdk::{
    env, near, require, AccountId, BorshStorageKey, Gas, NearToken, PanicOnDefault, Promise,
    PromiseError, PromiseOrValue,
};

pub const MAX_DOOR_KEYS: usize = 8;
pub const MAX_CLOCK_SKEW_MS: u64 = 15 * 60 * 1000;
const HOUR_MS: u64 = 3_600_000;

pub const STATUS_OPEN: &str = "open";
pub const STATUS_CLOSED: &str = "closed";
pub const STATUS_CANCELLED: &str = "cancelled";

pub const PAY_NONE: &str = "";
pub const PAY_OWED: &str = "owed";
pub const PAY_PROPOSING: &str = "proposing";
pub const PAY_APPROVING: &str = "approving";
pub const PAY_PAID: &str = "paid";
pub const PAY_FORFEIT: &str = "forfeit";

// Error strings match contract/core/types.go.
pub const ERR_UNAUTHORIZED: &str = "unauthorized";
pub const ERR_BAD_INPUT: &str = "bad input";
pub const ERR_EXISTS: &str = "already exists";
pub const ERR_NOT_FOUND: &str = "not found";
pub const ERR_CLOSED: &str = "event is not open";
pub const ERR_FULL: &str = "event is full";
pub const ERR_WRONG_AMOUNT: &str = "deposit amount does not match";
pub const ERR_PRIORITY_ONLY: &str = "priority RSVPs only: not enough past shows yet";
pub const ERR_DUPLICATE_GUEST: &str = "guest already has an RSVP for this event";
pub const ERR_BAD_SIGNATURE: &str = "door signature does not verify";
pub const ERR_UNKNOWN_DOOR_KEY: &str = "door key is not registered for this event";
pub const ERR_OUTSIDE_WINDOW: &str = "outside the check-in window";
pub const ERR_ALREADY_IN: &str = "already checked in";
pub const ERR_TOO_EARLY: &str = "too early to close";
pub const ERR_TOO_LATE: &str = "too late";

#[derive(BorshStorageKey)]
#[near]
enum Key {
    Events,
    Rsvps,
    Guests,
    Reputation,
}

#[near(serializers = [json, borsh])]
#[derive(Clone)]
pub struct Config {
    pub owner: AccountId,
    pub token: AccountId,
    pub relayer: AccountId,
}

#[near(serializers = [json, borsh])]
#[derive(Clone)]
pub struct Event {
    pub id: String,
    pub host: AccountId,
    pub title: String,
    pub deposit: U64,
    pub starts_at_ms: u64,
    pub doors_open_ms: u64,
    pub checkin_close_ms: u64,
    pub public_from_ms: u64,
    pub priority_min_shows: u32,
    pub capacity: u32,
    pub dao: AccountId,
    pub dao_bond: String,
    pub door_keys: Vec<String>,
    pub status: String,
    pub tickets: Vec<String>,
    pub going: u32,
    pub shows: u32,
    pub pot: U64,
    pub forfeit: U64,
    pub share_per_show: U64,
    pub dust: U64,
    pub paid_out: u32,
}

#[near(serializers = [json, borsh])]
#[derive(Clone)]
pub struct Rsvp {
    pub event: String,
    pub ticket: String,
    pub guest: String,
    pub pay_to: AccountId,
    pub sender: AccountId,
    pub deposit: U64,
    pub at_ms: u64,
    pub checked_in_at_ms: u64,
    pub door_key: String,
    pub payout: U64,
    pub pay_status: String,
    pub proposal_id: Option<u64>,
}

#[near(serializers = [json])]
pub struct CreateEventInput {
    pub id: String,
    pub title: String,
    pub deposit: String,
    pub starts_at_ms: u64,
    #[serde(default)]
    pub doors_open_ms: u64,
    pub checkin_close_ms: u64,
    #[serde(default)]
    pub public_from_ms: u64,
    #[serde(default)]
    pub priority_min_shows: u32,
    pub capacity: u32,
    pub dao: AccountId,
    #[serde(default)]
    pub dao_bond: String,
}

#[derive(Serialize, Deserialize)]
#[serde(crate = "near_sdk::serde")]
pub struct DepositMsg {
    pub event: String,
    pub ticket: String,
    #[serde(default)]
    pub guest: String,
    #[serde(default)]
    pub pay_to: String,
}

#[near(serializers = [json])]
pub struct CheckInInput {
    pub event: String,
    pub ticket: String,
    pub at_ms: u64,
    pub door_key: String,
    pub signature: String,
}

#[near(serializers = [json])]
pub struct SplitResult {
    pub pot: u64,
    pub forfeit: u64,
    pub share_per_show: u64,
    pub payout_per_show: u64,
    pub dust: u64,
    pub to_dao: u64,
}

/// The payout rule, in micro-USDC. See contract/core/split.go.
pub fn split(deposit: u64, going: u32, shows: u32) -> SplitResult {
    let shows = shows.min(going);
    let pot = deposit * going as u64;
    let forfeit = deposit * (going - shows) as u64;
    if shows == 0 {
        return SplitResult { pot, forfeit, share_per_show: 0, payout_per_show: 0, dust: 0, to_dao: pot };
    }
    let share = forfeit / shows as u64;
    let dust = forfeit % shows as u64;
    SplitResult { pot, forfeit, share_per_show: share, payout_per_show: deposit + share, dust, to_dao: dust }
}

/// The exact bytes a door phone signs.
pub fn checkin_message(event: &str, ticket: &str, at_ms: u64) -> String {
    format!("doorlist:checkin:v1|{event}|{ticket}|{at_ms}")
}

fn valid_event_id(s: &str) -> bool {
    (3..=40).contains(&s.len()) && s.bytes().all(|c| c.is_ascii_lowercase() || c.is_ascii_digit() || c == b'-')
}

fn valid_ticket(s: &str) -> bool {
    (8..=32).contains(&s.len()) && s.bytes().all(|c| c.is_ascii_uppercase() || (b'2'..=b'7').contains(&c))
}

fn valid_guest(s: &str) -> bool {
    if s.parse::<AccountId>().is_ok() && s.len() <= 64 {
        return true;
    }
    (4..=100).contains(&s.len()) && s.contains(':') && s.bytes().all(|c| c > b' ' && c <= b'~' && c != b'"' && c != b'\\')
}

fn parse_door_key(s: &str) -> Option<[u8; 32]> {
    let b58 = s.strip_prefix("ed25519:")?;
    let bytes = bs58_decode(b58)?;
    bytes.try_into().ok()
}

/// Minimal base58 (Bitcoin alphabet) decoder; keeps the wasm free of extra crates.
pub fn bs58_decode(s: &str) -> Option<Vec<u8>> {
    const ALPHA: &[u8] = b"123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz";
    let mut out: Vec<u8> = Vec::new();
    for c in s.bytes() {
        let mut carry = ALPHA.iter().position(|&a| a == c)? as u32;
        for b in out.iter_mut().rev() {
            carry += (*b as u32) * 58;
            *b = (carry & 0xff) as u8;
            carry >>= 8;
        }
        while carry > 0 {
            out.insert(0, (carry & 0xff) as u8);
            carry >>= 8;
        }
    }
    let zeros = s.bytes().take_while(|&c| c == b'1').count();
    let mut v = vec![0u8; zeros];
    v.extend(out);
    Some(v)
}

fn transfer_kind(token: &AccountId, receiver: &AccountId, amount: u64) -> serde_json::Value {
    json!({"Transfer": {"token_id": token, "receiver_id": receiver, "amount": amount.to_string(), "msg": null}})
}

#[near(contract_state)]
#[derive(PanicOnDefault)]
pub struct Doorlist {
    config: Config,
    events: LookupMap<String, Event>,
    rsvps: LookupMap<String, Rsvp>,
    guests: LookupMap<String, String>,
    reputation: LookupMap<String, u32>,
}

fn rkey(ev: &str, ticket: &str) -> String {
    format!("{ev}:{ticket}")
}

#[near]
impl Doorlist {
    #[init]
    pub fn init(owner: AccountId, token: AccountId, relayer: AccountId) -> Self {
        Self {
            config: Config { owner, token, relayer },
            events: LookupMap::new(Key::Events),
            rsvps: LookupMap::new(Key::Rsvps),
            guests: LookupMap::new(Key::Guests),
            reputation: LookupMap::new(Key::Reputation),
        }
    }

    // ---------- host ----------

    /// Args: {"input": {...CreateEventInput}}.
    pub fn create_event(&mut self, input: CreateEventInput) -> Event {
        self.create(input)
    }

    pub fn add_door_key(&mut self, event: String, key: String) {
        let mut ev = self.host_event(&event);
        require!(parse_door_key(&key).is_some(), ERR_BAD_INPUT);
        if !ev.door_keys.contains(&key) {
            require!(ev.door_keys.len() < MAX_DOOR_KEYS, ERR_BAD_INPUT);
            ev.door_keys.push(key);
        }
        self.events.insert(event, ev);
    }

    pub fn remove_door_key(&mut self, event: String, key: String) {
        let mut ev = self.host_event(&event);
        ev.door_keys.retain(|k| k != &key);
        self.events.insert(event, ev);
    }

    pub fn cancel_event(&mut self, event: String) -> Event {
        let mut ev = self.host_event(&event);
        require!(ev.status == STATUS_OPEN, ERR_CLOSED);
        require!(env::block_timestamp_ms() < ev.starts_at_ms, ERR_TOO_LATE);
        ev.status = STATUS_CANCELLED.into();
        for t in ev.tickets.clone() {
            let k = rkey(&event, &t);
            if let Some(mut r) = self.rsvps.get(&k).cloned() {
                r.payout = r.deposit;
                r.pay_status = PAY_OWED.into();
                self.rsvps.insert(k, r);
            }
        }
        self.events.insert(event, ev.clone());
        ev
    }

    // ---------- guests ----------

    /// NEP-141 receiver. msg: {"event","ticket","guest"?,"pay_to"?}.
    /// Returns the unused amount: 0 on success, everything on rejection.
    pub fn ft_on_transfer(&mut self, sender_id: AccountId, amount: U128, msg: String) -> PromiseOrValue<U128> {
        match self.on_deposit(sender_id, amount.0, &msg) {
            Ok(()) => PromiseOrValue::Value(U128(0)),
            Err(e) => {
                env::log_str(&format!("doorlist: deposit rejected: {e}"));
                PromiseOrValue::Value(amount)
            }
        }
    }

    // ---------- door ----------

    /// Args: {"input": {...CheckInInput}}.
    pub fn check_in(&mut self, input: CheckInInput) -> Rsvp {
        let CheckInInput { event, ticket, at_ms, door_key, signature } = input;
        let mut ev = self.events.get(&event).cloned().unwrap_or_else(|| env::panic_str(ERR_NOT_FOUND));
        require!(ev.status == STATUS_OPEN, ERR_CLOSED);
        require!(ev.door_keys.contains(&door_key), ERR_UNKNOWN_DOOR_KEY);
        let pk = parse_door_key(&door_key).unwrap_or_else(|| env::panic_str(ERR_BAD_INPUT));
        let sig: [u8; 64] = bs58_decode(&signature)
            .and_then(|b| b.try_into().ok())
            .unwrap_or_else(|| env::panic_str(ERR_BAD_SIGNATURE));
        require!(env::ed25519_verify(&sig, checkin_message(&event, &ticket, at_ms).as_bytes(), &pk), ERR_BAD_SIGNATURE);
        let now = env::block_timestamp_ms();
        require!(at_ms >= ev.doors_open_ms && at_ms <= ev.checkin_close_ms && now >= ev.doors_open_ms, ERR_OUTSIDE_WINDOW);
        require!(now <= at_ms + MAX_CLOCK_SKEW_MS && at_ms <= now + MAX_CLOCK_SKEW_MS, ERR_OUTSIDE_WINDOW);
        let k = rkey(&event, &ticket);
        let mut r = self.rsvps.get(&k).cloned().unwrap_or_else(|| env::panic_str(ERR_NOT_FOUND));
        require!(r.checked_in_at_ms == 0, ERR_ALREADY_IN);
        r.checked_in_at_ms = at_ms;
        r.door_key = door_key;
        self.rsvps.insert(k, r.clone());
        ev.shows += 1;
        self.events.insert(event.clone(), ev);
        let n = self.reputation.get(&r.guest).copied().unwrap_or(0);
        self.reputation.insert(r.guest.clone(), n + 1);
        env::log_str(&format!(r#"EVENT_JSON:{{"standard":"doorlist","version":"1","event":"check_in","data":{{"event":"{event}","ticket":"{ticket}"}}}}"#));
        r
    }

    // ---------- close and payouts ----------

    pub fn close_event(&mut self, event: String) -> Event {
        let mut ev = self.events.get(&event).cloned().unwrap_or_else(|| env::panic_str(ERR_NOT_FOUND));
        require!(ev.status == STATUS_OPEN, ERR_CLOSED);
        let now = env::block_timestamp_ms();
        let is_host = env::predecessor_account_id() == ev.host;
        require!((is_host && now >= ev.doors_open_ms) || now > ev.checkin_close_ms, ERR_TOO_EARLY);
        let s = split(ev.deposit.0, ev.going, ev.shows);
        ev.forfeit = U64(s.forfeit);
        ev.share_per_show = U64(s.share_per_show);
        ev.dust = U64(s.dust);
        ev.status = STATUS_CLOSED.into();
        for t in ev.tickets.clone() {
            let k = rkey(&event, &t);
            if let Some(mut r) = self.rsvps.get(&k).cloned() {
                if r.checked_in_at_ms != 0 {
                    r.payout = U64(s.payout_per_show);
                    r.pay_status = PAY_OWED.into();
                } else {
                    r.payout = U64(0);
                    r.pay_status = PAY_FORFEIT.into();
                }
                self.rsvps.insert(k, r);
            }
        }
        self.events.insert(event, ev.clone());
        ev
    }

    /// Files up to `limit` payout proposals in the DAO. Anyone can call it.
    pub fn payout_batch(&mut self, event: String, limit: u32) -> u32 {
        let ev = self.events.get(&event).cloned().unwrap_or_else(|| env::panic_str(ERR_NOT_FOUND));
        require!(ev.status != STATUS_OPEN, ERR_TOO_EARLY);
        // Each payout needs about 115 Tgas (proposal, approval, transfer, callbacks),
        // so two fit in one 300 Tgas call.
        let limit = if limit == 0 || limit > 2 { 2 } else { limit };
        let bond = NearToken::from_yoctonear(ev.dao_bond.parse::<u128>().unwrap_or(0));
        let mut n = 0u32;
        for t in ev.tickets.iter() {
            if n >= limit {
                break;
            }
            let k = rkey(&event, t);
            let Some(mut r) = self.rsvps.get(&k).cloned() else { continue };
            if r.pay_status != PAY_OWED {
                continue;
            }
            r.pay_status = PAY_PROPOSING.into();
            let kind = transfer_kind(&self.config.token, &r.pay_to, r.payout.0);
            self.rsvps.insert(k, r);
            let args = json!({"proposal": {"description": format!("Doorlist payout · {event} · ticket {t}"), "kind": kind}});
            Promise::new(ev.dao.clone())
                .function_call("add_proposal".to_string(), args.to_string().into_bytes(), bond, Gas::from_tgas(25))
                .then(
                    Self::ext(env::current_account_id())
                        .with_static_gas(Gas::from_tgas(80))
                        .on_payout_proposed(event.clone(), t.clone()),
                )
                .detach();
            n += 1;
        }
        n
    }

    #[private]
    pub fn on_payout_proposed(&mut self, event: String, ticket: String, #[callback_result] id: Result<u64, PromiseError>) {
        let ev = self.events.get(&event).cloned().unwrap_or_else(|| env::panic_str(ERR_NOT_FOUND));
        let k = rkey(&event, &ticket);
        let mut r = self.rsvps.get(&k).cloned().unwrap_or_else(|| env::panic_str(ERR_NOT_FOUND));
        require!(r.pay_status == PAY_PROPOSING, ERR_BAD_INPUT);
        let Ok(id) = id else {
            r.pay_status = PAY_OWED.into(); // retry with the next batch
            self.rsvps.insert(k, r);
            return;
        };
        r.proposal_id = Some(id);
        r.pay_status = PAY_APPROVING.into();
        let kind = transfer_kind(&self.config.token, &r.pay_to, r.payout.0);
        self.rsvps.insert(k, r);
        let args = json!({"id": id, "action": "VoteApprove", "proposal": kind, "memo": null});
        Promise::new(ev.dao)
            .function_call("act_proposal".to_string(), args.to_string().into_bytes(), NearToken::from_yoctonear(0), Gas::from_tgas(50))
            .then(
                Self::ext(env::current_account_id())
                    .with_static_gas(Gas::from_tgas(8))
                    .on_payout_approved(event, ticket),
            )
            .detach();
    }

    #[private]
    pub fn on_payout_approved(&mut self, event: String, ticket: String, #[callback_result] res: Result<(), PromiseError>) {
        let k = rkey(&event, &ticket);
        let mut r = self.rsvps.get(&k).cloned().unwrap_or_else(|| env::panic_str(ERR_NOT_FOUND));
        require!(r.pay_status == PAY_APPROVING, ERR_BAD_INPUT);
        if res.is_err() {
            // The proposal exists; the council can still approve it in the DAO.
            return;
        }
        r.pay_status = PAY_PAID.into();
        self.rsvps.insert(k, r);
        if let Some(mut ev) = self.events.get(&event).cloned() {
            ev.paid_out += 1;
            self.events.insert(event, ev);
        }
    }

    // ---------- views ----------

    pub fn get_config(&self) -> Config {
        self.config.clone()
    }
    pub fn get_event(&self, event: String) -> Option<Event> {
        self.events.get(&event).cloned()
    }
    pub fn get_rsvp(&self, event: String, ticket: String) -> Option<Rsvp> {
        self.rsvps.get(&rkey(&event, &ticket)).cloned()
    }
    pub fn list_rsvps(&self, event: String, from: u32, limit: u32) -> Vec<Rsvp> {
        let Some(ev) = self.events.get(&event) else { return vec![] };
        ev.tickets
            .iter()
            .skip(from as usize)
            .take(limit.min(100) as usize)
            .filter_map(|t| self.rsvps.get(&rkey(&event, t)).cloned())
            .collect()
    }
    pub fn get_shows(&self, account: String) -> u32 {
        self.reputation.get(&account).copied().unwrap_or(0)
    }
    pub fn can_rsvp(&self, event: String, account: String) -> String {
        match self.events.get(&event) {
            None => ERR_NOT_FOUND.into(),
            Some(ev) => match self.check_can_rsvp(ev, &account) {
                Ok(()) => "ok".into(),
                Err(e) => e.into(),
            },
        }
    }
    pub fn get_split(&self, deposit: U64, going: u32, shows: u32) -> SplitResult {
        split(deposit.0, going, shows)
    }
}

impl Doorlist {
    fn host_event(&self, id: &str) -> Event {
        let ev = self.events.get(id).cloned().unwrap_or_else(|| env::panic_str(ERR_NOT_FOUND));
        require!(env::predecessor_account_id() == ev.host, ERR_UNAUTHORIZED);
        ev
    }

    fn create(&mut self, mut i: CreateEventInput) -> Event {
        let now = env::block_timestamp_ms();
        let dep: u64 = i.deposit.parse().unwrap_or(0);
        require!(dep > 0, ERR_BAD_INPUT);
        require!(valid_event_id(&i.id) && !i.title.is_empty() && i.title.len() <= 120, ERR_BAD_INPUT);
        require!(i.starts_at_ms > now && i.checkin_close_ms > i.starts_at_ms, ERR_BAD_INPUT);
        require!(i.capacity > 0 && i.capacity <= 2000, ERR_BAD_INPUT);
        if i.doors_open_ms == 0 {
            i.doors_open_ms = i.starts_at_ms - HOUR_MS;
        }
        require!(i.doors_open_ms <= i.starts_at_ms && i.public_from_ms <= i.starts_at_ms, ERR_BAD_INPUT);
        if i.dao_bond.is_empty() {
            i.dao_bond = "0".into();
        }
        require!(i.dao_bond.parse::<u128>().is_ok(), ERR_BAD_INPUT);
        require!(!self.events.contains_key(&i.id), ERR_EXISTS);
        let ev = Event {
            id: i.id.clone(),
            host: env::predecessor_account_id(),
            title: i.title,
            deposit: U64(dep),
            starts_at_ms: i.starts_at_ms,
            doors_open_ms: i.doors_open_ms,
            checkin_close_ms: i.checkin_close_ms,
            public_from_ms: i.public_from_ms,
            priority_min_shows: i.priority_min_shows,
            capacity: i.capacity,
            dao: i.dao,
            dao_bond: i.dao_bond,
            door_keys: vec![],
            status: STATUS_OPEN.into(),
            tickets: vec![],
            going: 0,
            shows: 0,
            pot: U64(0),
            forfeit: U64(0),
            share_per_show: U64(0),
            dust: U64(0),
            paid_out: 0,
        };
        self.events.insert(i.id.clone(), ev.clone());
        env::log_str(&format!(r#"EVENT_JSON:{{"standard":"doorlist","version":"1","event":"event_created","data":{{"id":"{}","host":"{}"}}}}"#, ev.id, ev.host));
        ev
    }

    fn check_can_rsvp(&self, ev: &Event, guest: &str) -> Result<(), &'static str> {
        let now = env::block_timestamp_ms();
        if ev.status != STATUS_OPEN {
            return Err(ERR_CLOSED);
        }
        if now >= ev.starts_at_ms {
            return Err(ERR_TOO_LATE);
        }
        if ev.going >= ev.capacity {
            return Err(ERR_FULL);
        }
        if now < ev.public_from_ms && self.reputation.get(guest).copied().unwrap_or(0) < ev.priority_min_shows {
            return Err(ERR_PRIORITY_ONLY);
        }
        if self.guests.contains_key(&format!("{}:{}", ev.id, guest)) {
            return Err(ERR_DUPLICATE_GUEST);
        }
        Ok(())
    }

    fn on_deposit(&mut self, sender: AccountId, amount: u128, msg: &str) -> Result<(), &'static str> {
        if env::predecessor_account_id() != self.config.token {
            return Err(ERR_UNAUTHORIZED);
        }
        let m: DepositMsg = serde_json::from_str(msg).map_err(|_| ERR_BAD_INPUT)?;
        if !valid_ticket(&m.ticket) {
            return Err(ERR_BAD_INPUT);
        }
        let guest = if m.guest.is_empty() { sender.to_string() } else { m.guest };
        let pay_to_s = if m.pay_to.is_empty() { guest.clone() } else { m.pay_to };
        if (guest != sender.as_str() || pay_to_s != sender.as_str()) && sender != self.config.relayer {
            return Err(ERR_UNAUTHORIZED);
        }
        let pay_to: AccountId = pay_to_s.parse().map_err(|_| ERR_BAD_INPUT)?;
        if !valid_guest(&guest) {
            return Err(ERR_BAD_INPUT);
        }
        let mut ev = self.events.get(&m.event).cloned().ok_or(ERR_NOT_FOUND)?;
        if amount != ev.deposit.0 as u128 {
            return Err(ERR_WRONG_AMOUNT);
        }
        self.check_can_rsvp(&ev, &guest)?;
        let k = rkey(&ev.id, &m.ticket);
        if self.rsvps.contains_key(&k) {
            return Err(ERR_EXISTS);
        }
        let r = Rsvp {
            event: ev.id.clone(),
            ticket: m.ticket.clone(),
            guest: guest.clone(),
            pay_to,
            sender,
            deposit: ev.deposit,
            at_ms: env::block_timestamp_ms(),
            checked_in_at_ms: 0,
            door_key: String::new(),
            payout: U64(0),
            pay_status: PAY_NONE.into(),
            proposal_id: None,
        };
        self.rsvps.insert(k, r);
        self.guests.insert(format!("{}:{}", ev.id, guest), m.ticket.clone());
        ev.tickets.push(m.ticket);
        ev.going += 1;
        ev.pot = U64(ev.pot.0 + ev.deposit.0);
        // The pot lives in the event's DAO, not here.
        Promise::new(self.config.token.clone()).function_call(
            "ft_transfer".to_string(),
            json!({"receiver_id": ev.dao, "amount": ev.deposit.0.to_string(), "memo": format!("doorlist {}", ev.id)}).to_string().into_bytes(),
            NearToken::from_yoctonear(1),
            Gas::from_tgas(15),
        )
        .detach();
        self.events.insert(ev.id.clone(), ev);
        Ok(())
    }
}

#[cfg(test)]
mod tests;
