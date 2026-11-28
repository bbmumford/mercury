/*
 * Copyright (c) 2026 Brandon Miller-Mumford.
 * Licensed under the MIT License; see LICENSE.
 */
package arbitration

import (
	"bytes"
	"crypto/ed25519"
	"fmt"

	"github.com/bbmumford/plutus/reputation"
)

// DisputeReputationAppID is the application every dispute reputation
// record names.
const DisputeReputationAppID = "mercury"

// DisputeScores returns the reputation scores a ruling's outcome gives the
// buyer and the seller: the vindicated party +1 and the other -1, and 0 to
// both for a split (or an outcome this package does not name).
func DisputeScores(outcome RulingOutcome) (buyer, seller int32) {
	switch outcome {
	case RulingBuyerWins:
		return 1, -1
	case RulingSellerWins:
		return -1, 1
	default:
		return 0, 0
	}
}

// DisputeReputationRecords builds the unsigned buyer's and seller's dispute
// reputation records for r ruling on orderID: each names its party as
// subject, the ruling's arbitrator as recorder, the party's DisputeScores
// score, DisputeReputationAppID, orderID as TxHash and the ruling's
// Timestamp (reputation.DisputeRecord). SignDisputeReputation signs these
// records and VerifyDisputeReputation checks signatures over them, so the
// arbitrator and the settling node build the same bytes.
func DisputeReputationRecords(r *ArbitratorRuling, orderID string, buyer, seller []byte) (buyerRec, sellerRec reputation.ReputationRecord) {
	buyerScore, sellerScore := DisputeScores(r.Outcome)
	buyerRec = reputation.DisputeRecord(buyer, r.ArbitratorPubKey, buyerScore, DisputeReputationAppID, orderID, r.Timestamp)
	sellerRec = reputation.DisputeRecord(seller, r.ArbitratorPubKey, sellerScore, DisputeReputationAppID, orderID, r.Timestamp)
	return buyerRec, sellerRec
}

// SignDisputeReputation signs the buyer's and the seller's dispute
// reputation records for r ruling on orderID with the arbitrator's private
// key and sets r.BuyerReputationSignature and r.SellerReputationSignature.
// The key must be the one r.ArbitratorPubKey names, and r's Outcome and
// Timestamp must be final: the records are built from them. It does not
// sign the ruling itself (SignRuling does).
func SignDisputeReputation(r *ArbitratorRuling, orderID string, buyer, seller []byte, privKey ed25519.PrivateKey) error {
	if len(privKey) != ed25519.PrivateKeySize {
		return fmt.Errorf("arbitration: invalid private key size %d", len(privKey))
	}
	if !bytes.Equal(privKey.Public().(ed25519.PublicKey), r.ArbitratorPubKey) {
		return fmt.Errorf("arbitration: the reputation signing key is not the ruling's arbitrator key")
	}
	buyerRec, sellerRec := DisputeReputationRecords(r, orderID, buyer, seller)
	if err := reputation.SignRecord(&buyerRec, privKey); err != nil {
		return fmt.Errorf("arbitration: sign the buyer's dispute record: %w", err)
	}
	if err := reputation.SignRecord(&sellerRec, privKey); err != nil {
		return fmt.Errorf("arbitration: sign the seller's dispute record: %w", err)
	}
	r.BuyerReputationSignature = buyerRec.Signature
	r.SellerReputationSignature = sellerRec.Signature
	return nil
}

// VerifyDisputeReputation reports whether r carries the arbitrator's valid
// signatures over the buyer's and the seller's dispute reputation records
// for orderID, and returns those records with their signatures attached,
// ready to store. Each signature is checked against its own party's record,
// so swapped, missing or foreign signatures fail.
func VerifyDisputeReputation(r *ArbitratorRuling, orderID string, buyer, seller []byte) (buyerRec, sellerRec reputation.ReputationRecord, ok bool) {
	buyerRec, sellerRec = DisputeReputationRecords(r, orderID, buyer, seller)
	buyerRec.Signature = r.BuyerReputationSignature
	sellerRec.Signature = r.SellerReputationSignature
	if !reputation.VerifyRecord(&buyerRec) || !reputation.VerifyRecord(&sellerRec) {
		return reputation.ReputationRecord{}, reputation.ReputationRecord{}, false
	}
	return buyerRec, sellerRec, true
}
