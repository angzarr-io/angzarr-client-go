// Package angzarr — deterministic aggregate-root derivation.
//
// Cross-language identity helpers. Spec HIGH-2.2:
// `core/main/.plan/cross-language-spec.md` §2.9.
//
// Every language derives aggregate roots from a business key using
// RFC-4122 UUIDv5 with the OID namespace and a fixed seed format
// "angzarr" + domain + key. Byte-equality is pinned:
//
//	compute_root("player", "alice@x.com") = 8cf1fb5d-45ce-58c2-a7e4-34359eb42d7c
//
// (`examples-rust/main/angzarr-client-rust/src/identity.rs:71`).
package angzarr

import (
	pb "github.com/benjaminabbitt/angzarr/client/go/proto/angzarr_client/proto/angzarr/v1"
	"github.com/google/uuid"
)

// AngzarrOIDNamespace is the RFC-4122 OID namespace UUID used by
// ComputeRoot. Same constant in Py / Rs / Cs.
var AngzarrOIDNamespace = uuid.NameSpaceOID

// InventoryProductNamespace is the RFC-4122 DNS namespace UUID used by
// InventoryProductRoot. Mirrors Py `identity.INVENTORY_PRODUCT_NAMESPACE`
// and Rs `identity::INVENTORY_PRODUCT_NAMESPACE`.
var InventoryProductNamespace = uuid.NameSpaceDNS

// ComputeRoot derives an aggregate root from a domain + business key.
//
// Implementation: UUIDv5 in the OID namespace over the seed string
// "angzarr" + domain + key. Cross-language byte-equal output.
func ComputeRoot(domain, key string) uuid.UUID {
	seed := "angzarr" + domain + key
	return uuid.NewSHA1(AngzarrOIDNamespace, []byte(seed))
}

// InventoryProductRoot is the namespaced variant for inventory products.
// Uses the DNS namespace per the cross-language convention.
func InventoryProductRoot(id string) uuid.UUID {
	return uuid.NewSHA1(InventoryProductNamespace, []byte(id))
}

// CustomerRoot derives a customer aggregate root from an email.
func CustomerRoot(email string) uuid.UUID { return ComputeRoot("customer", email) }

// ProductRoot derives a product aggregate root from a SKU.
func ProductRoot(sku string) uuid.UUID { return ComputeRoot("product", sku) }

// OrderRoot derives an order aggregate root from an order ID.
func OrderRoot(orderID string) uuid.UUID { return ComputeRoot("order", orderID) }

// InventoryRoot derives an inventory aggregate root from a product ID.
func InventoryRoot(productID string) uuid.UUID { return ComputeRoot("inventory", productID) }

// CartRoot derives a cart aggregate root from a customer ID.
func CartRoot(customerID string) uuid.UUID { return ComputeRoot("cart", customerID) }

// FulfillmentRoot derives a fulfillment aggregate root from an order ID.
func FulfillmentRoot(orderID string) uuid.UUID { return ComputeRoot("fulfillment", orderID) }

// ToProtoBytes converts a uuid.UUID into the wire-format pb.UUID. Mirrors
// Py `identity.to_proto_bytes` and Cs `Identity.ToProtoBytes`. Byte order
// is big-endian (uuid.UUID is already big-endian internally).
func ToProtoBytes(u uuid.UUID) *pb.UUID {
	out := make([]byte, 16)
	copy(out, u[:])
	return &pb.UUID{Value: out}
}
