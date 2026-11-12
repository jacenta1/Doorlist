use super::*;
use ed25519_dalek::{Signer, SigningKey};
use near_sdk::test_utils::VMContextBuilder;
use near_sdk::{testing_env, AccountId};

const T0: u64 = 1_800_000_000_000;
const H: u64 = 3_600_000;

fn acc(s: &str) -> AccountId {
    s.parse().unwrap()
}

fn ctx(pred: &str, now_ms: u64) {
    let mut b = VMContextBuilder::new();
    b.current_account_id(acc("doorlist.testnet"))
        .predecessor_account_id(acc(pred))
        .block_timestamp(now_ms * 1_000_000)
        .attached_deposit(NearToken::from_yoctonear(0));
    testing_env!(b.build());
}

fn bs58(bytes: &[u8]) -> String {
    const ALPHA: &[u8] = b"123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz";
    let mut digits: Vec<u32> = vec![0];
    for &b in bytes {
        let mut carry = b as u32;
        for d in digits.iter_mut() {
            carry += *d << 8;
            *d = carry % 58;
            carry /= 58;
        }
        while carry > 0 {
            digits.push(carry % 58);
            carry /= 58;
        }
    }
    let mut s: String = bytes.iter().take_while(|&&b| b == 0).map(|_| '1').collect();
    for d in digits.iter().rev() {
        s.push(ALPHA[*d as usize] as char);
    }
    s
}

struct Door(SigningKey);
impl Door {
    fn new(seed: u8) -> Self {
        Door(SigningKey::from_bytes(&[seed; 32]))
    }
    fn key(&self) -> String {
        format!("ed25519:{}", bs58(self.0.verifying_key().as_bytes()))
    }
    fn scan(&self, ticket: &str, at: u64) -> CheckInInput {
        let sig = self.0.sign(checkin_message("night", ticket, at).as_bytes());
        CheckInInput { event: "night".into(), ticket: ticket.into(), at_ms: at, door_key: self.key(), signature: bs58(&sig.to_bytes()) }
    }
}

fn setup() -> Doorlist {
    ctx("owner.testnet", T0);
    let mut c = Doorlist::init(acc("owner.testnet"), acc("usdc.testnet"), acc("relay.testnet"));
    ctx("host.testnet", T0);
    c.create_event(CreateEventInput {
        id: "night".into(),
        title: "Builders Night".into(),
        deposit: "10000000".into(),
        starts_at_ms: T0 + 48 * H,
        doors_open_ms: 0,
        checkin_close_ms: T0 + 51 * H,
        public_from_ms: T0 + 24 * H,
        priority_min_shows: 1,
        capacity: 3,
        dao: acc("club.sputnik-dao.near"),
        dao_bond: String::new(),
    });
    c
}

fn deposit(c: &mut Doorlist, sender: &str, msg: &str, amount: u128) -> u128 {
    ctx("usdc.testnet", T0 + 25 * H);
    match c.ft_on_transfer(acc(sender), U128(amount), msg.into()) {
        PromiseOrValue::Value(v) => v.0,
        _ => panic!("expected value"),
    }
}

fn rsvp(c: &mut Doorlist, guest: &str, ticket: &str) {
    // Guests who paid from another chain are paid through the relayer's outbox.
    let pay_to = if guest.contains(':') { "outbox.relay.testnet" } else { guest };
    let m = format!(r#"{{"event":"night","ticket":"{ticket}","guest":"{guest}","pay_to":"{pay_to}"}}"#);
    assert_eq!(deposit(c, "relay.testnet", &m, 10_000_000), 0, "rsvp {guest}");
}

#[test]
fn split_vectors_match_go() {
    let raw = include_str!("../../../testdata/split-vectors.json");
    let v: serde_json::Value = serde_json::from_str(raw).unwrap();
    for case in v.as_array().unwrap() {
        let s = split(case["deposit"].as_u64().unwrap(), case["going"].as_u64().unwrap() as u32, case["shows"].as_u64().unwrap() as u32);
        assert_eq!(s.payout_per_show, case["payout_per_show"].as_u64().unwrap(), "{}", case["name"]);
        assert_eq!(s.forfeit, case["forfeit"].as_u64().unwrap(), "{}", case["name"]);
        assert_eq!(s.dust, case["dust"].as_u64().unwrap(), "{}", case["name"]);
        assert_eq!(s.to_dao, case["to_dao"].as_u64().unwrap(), "{}", case["name"]);
    }
}

#[test]
fn full_flow() {
    let mut c = setup();
    rsvp(&mut c, "ana.near", "AAAABBBB");
    rsvp(&mut c, "base:0x11aa22bb33cc44dd55ee66ff77889900aabbccdd", "AAAACCCC");
    rsvp(&mut c, "cy.near", "AAAADDDD");
    let door = Door::new(7);
    ctx("host.testnet", T0 + 25 * H);
    c.add_door_key("night".into(), door.key());
    let at = T0 + 48 * H;
    ctx("relay.testnet", at);
    c.check_in(door.scan("AAAABBBB", at));
    c.check_in(door.scan("AAAADDDD", at));
    assert_eq!(c.get_shows("ana.near".into()), 1);
    ctx("host.testnet", at + H);
    let ev = c.close_event("night".into());
    assert_eq!(ev.share_per_show.0, 5_000_000);
    let ana = c.get_rsvp("night".into(), "AAAABBBB".into()).unwrap();
    assert_eq!((ana.payout.0, ana.pay_status.as_str()), (15_000_000, PAY_OWED));
    let flake = c.get_rsvp("night".into(), "AAAACCCC".into()).unwrap();
    assert_eq!(flake.pay_status, PAY_FORFEIT);
    ctx("anyone.testnet", at + H);
    assert_eq!(c.payout_batch("night".into(), 10), 2);
    assert_eq!(c.payout_batch("night".into(), 10), 0, "nothing proposed twice");
    ctx("doorlist.testnet", at + H);
    c.on_payout_proposed("night".into(), "AAAABBBB".into(), Ok(41));
    c.on_payout_approved("night".into(), "AAAABBBB".into(), Ok(()));
    let ana = c.get_rsvp("night".into(), "AAAABBBB".into()).unwrap();
    assert_eq!((ana.pay_status.as_str(), ana.proposal_id), (PAY_PAID, Some(41)));
}

#[test]
fn deposits_that_must_bounce() {
    let mut c = setup();
    let ok = r#"{"event":"night","ticket":"AAAABBBB","guest":"ana.near"}"#;
    assert_eq!(deposit(&mut c, "relay.testnet", ok, 9_000_000), 9_000_000, "wrong amount");
    assert_eq!(deposit(&mut c, "mallory.near", ok, 10_000_000), 10_000_000, "stranger pays for someone");
    let redirect = r#"{"event":"night","ticket":"AAAABBBB","pay_to":"thief.near"}"#;
    assert_eq!(deposit(&mut c, "eve.near", redirect, 10_000_000), 10_000_000, "payout redirect");
    assert_eq!(deposit(&mut c, "relay.testnet", "not json", 10_000_000), 10_000_000);
    ctx("fake-usdc.testnet", T0 + 25 * H);
    assert!(matches!(c.ft_on_transfer(acc("ana.near"), U128(10_000_000), ok.into()), PromiseOrValue::Value(U128(10_000_000))), "fake token");
    rsvp(&mut c, "ana.near", "AAAABBBB");
    assert_eq!(deposit(&mut c, "relay.testnet", r#"{"event":"night","ticket":"AAAAEEEE","guest":"ana.near"}"#, 10_000_000), 10_000_000, "same guest twice");
}

#[test]
fn priority_window() {
    let mut c = setup();
    ctx("usdc.testnet", T0 + H);
    let m = r#"{"event":"night","ticket":"AAAABBBB","guest":"new.near"}"#;
    assert!(matches!(c.ft_on_transfer(acc("relay.testnet"), U128(10_000_000), m.into()), PromiseOrValue::Value(U128(10_000_000))));
    assert_eq!(c.can_rsvp("night".into(), "new.near".into()), ERR_PRIORITY_ONLY);
}

#[test]
#[should_panic(expected = "door signature does not verify")]
fn fake_check_in_is_refused() {
    let mut c = setup();
    rsvp(&mut c, "ana.near", "AAAABBBB");
    let door = Door::new(7);
    ctx("host.testnet", T0 + 25 * H);
    c.add_door_key("night".into(), door.key());
    let at = T0 + 48 * H;
    ctx("relay.testnet", at);
    let mut s = door.scan("AAAACCCC", at);
    s.ticket = "AAAABBBB".into();
    c.check_in(s);
}

#[test]
#[should_panic(expected = "door key is not registered for this event")]
fn unregistered_door_is_refused() {
    let mut c = setup();
    rsvp(&mut c, "ana.near", "AAAABBBB");
    let at = T0 + 48 * H;
    ctx("relay.testnet", at);
    c.check_in(Door::new(9).scan("AAAABBBB", at));
}

#[test]
#[should_panic(expected = "unauthorized")]
fn non_host_cannot_add_a_door() {
    let mut c = setup();
    ctx("mallory.testnet", T0 + H);
    c.add_door_key("night".into(), Door::new(9).key());
}

#[test]
#[should_panic(expected = "already checked in")]
fn replayed_check_in_is_refused() {
    let mut c = setup();
    rsvp(&mut c, "ana.near", "AAAABBBB");
    let door = Door::new(7);
    ctx("host.testnet", T0 + 25 * H);
    c.add_door_key("night".into(), door.key());
    let at = T0 + 48 * H;
    ctx("relay.testnet", at);
    let s = door.scan("AAAABBBB", at);
    let replay = CheckInInput { ..door.scan("AAAABBBB", at) };
    c.check_in(s);
    c.check_in(replay);
}

#[test]
#[should_panic(expected = "outside the check-in window")]
fn stale_signature_is_refused() {
    let mut c = setup();
    rsvp(&mut c, "ana.near", "AAAABBBB");
    let door = Door::new(7);
    ctx("host.testnet", T0 + 25 * H);
    c.add_door_key("night".into(), door.key());
    ctx("relay.testnet", T0 + 49 * H);
    c.check_in(door.scan("AAAABBBB", T0 + 48 * H));
}

#[test]
#[should_panic(expected = "too early to close")]
fn non_host_cannot_close_early() {
    let mut c = setup();
    ctx("anyone.testnet", T0 + 48 * H);
    c.close_event("night".into());
}

#[test]
#[should_panic(expected = "bad input")]
fn double_refund_is_refused() {
    let mut c = setup();
    rsvp(&mut c, "ana.near", "AAAABBBB");
    let door = Door::new(7);
    ctx("host.testnet", T0 + 25 * H);
    c.add_door_key("night".into(), door.key());
    let at = T0 + 48 * H;
    ctx("relay.testnet", at);
    c.check_in(door.scan("AAAABBBB", at));
    ctx("host.testnet", at);
    c.close_event("night".into());
    c.payout_batch("night".into(), 10);
    ctx("doorlist.testnet", at);
    c.on_payout_proposed("night".into(), "AAAABBBB".into(), Ok(1));
    c.on_payout_approved("night".into(), "AAAABBBB".into(), Ok(()));
    // A second approval callback for the same ticket must not pay again.
    c.on_payout_proposed("night".into(), "AAAABBBB".into(), Ok(2));
}

#[test]
fn checkin_message_matches_go() {
    assert_eq!(checkin_message("night", "AAAABBBB", 42), "doorlist:checkin:v1|night|AAAABBBB|42");
    assert_eq!(bs58_decode("StV1DL6CwTryKyV").unwrap(), b"hello world");
}
