#!/usr/bin/env python3
"""
Sign an A2A AgentCard with a private key.

Usage:
    python sign-agent-card.py <agent-card.json> <private-key.pem> [--key-id KEY_ID]

Example:
    python sign-agent-card.py weather-agent-card.json private-key.pem --key-id my-key
"""

import sys
import json
import base64
import argparse
from datetime import datetime, timezone

try:
    from cryptography.hazmat.primitives import hashes, serialization
    from cryptography.hazmat.primitives.asymmetric import padding, rsa
    from cryptography.hazmat.backends import default_backend
except ImportError:
    print("Error: cryptography library not found.")
    print("Install it with: pip install cryptography")
    sys.exit(1)


def load_private_key(key_path):
    """Load a private key from a PEM file."""
    try:
        with open(key_path, 'rb') as f:
            private_key = serialization.load_pem_private_key(
                f.read(),
                password=None,
                backend=default_backend()
            )
        return private_key
    except Exception as e:
        print(f"Error loading private key: {e}")
        sys.exit(1)


def create_canonical_json(card_data):
    """
    Create a canonical JSON representation for signing.
    
    According to A2A spec, the signature is over the card's JSON
    representation excluding the signature field itself.
    """
    # Remove signature if it exists
    card_copy = card_data.copy()
    card_copy.pop('signature', None)
    
    # Create canonical JSON with sorted keys and no whitespace
    canonical = json.dumps(card_copy, sort_keys=True, separators=(',', ':'))
    return canonical.encode('utf-8')


def sign_card(card_data, private_key, key_id=None):
    """Sign an agent card with a private key."""
    # Create canonical JSON
    canonical_json = create_canonical_json(card_data)
    
    # Determine algorithm based on key type
    if isinstance(private_key, rsa.RSAPrivateKey):
        algorithm = 'RS256'
        signature_bytes = private_key.sign(
            canonical_json,
            padding.PKCS1v15(),
            hashes.SHA256()
        )
    else:
        print("Error: Unsupported key type. Only RSA keys are currently supported.")
        sys.exit(1)
    
    # Encode signature as base64
    signature_b64 = base64.b64encode(signature_bytes).decode('utf-8')
    
    # Create signature object
    signature_obj = {
        'algorithm': algorithm,
        'value': signature_b64,
    }
    
    if key_id:
        signature_obj['keyId'] = key_id
    
    # Add timestamp
    signature_obj['timestamp'] = datetime.now(timezone.utc).isoformat().replace('+00:00', 'Z')
    
    # Add signature to card
    card_data['signature'] = signature_obj
    
    return card_data


def main():
    parser = argparse.ArgumentParser(
        description='Sign an A2A AgentCard with a private key'
    )
    parser.add_argument(
        'card_file',
        help='Path to the agent card JSON file'
    )
    parser.add_argument(
        'key_file',
        help='Path to the private key PEM file'
    )
    parser.add_argument(
        '--key-id',
        help='Key ID to include in the signature (optional)'
    )
    parser.add_argument(
        '--output',
        help='Output file (default: overwrite input file)'
    )
    
    args = parser.parse_args()
    
    # Load agent card
    try:
        with open(args.card_file, 'r') as f:
            card_data = json.load(f)
    except Exception as e:
        print(f"Error loading agent card: {e}")
        sys.exit(1)
    
    # Load private key
    private_key = load_private_key(args.key_file)
    
    # Sign the card
    signed_card = sign_card(card_data, private_key, args.key_id)
    
    # Write output
    output_file = args.output or args.card_file
    try:
        with open(output_file, 'w') as f:
            json.dump(signed_card, f, indent=2)
        print(f"Successfully signed agent card and wrote to {output_file}")
        print(f"Key ID: {signed_card['signature'].get('keyId', '(none)')}")
        print(f"Algorithm: {signed_card['signature']['algorithm']}")
    except Exception as e:
        print(f"Error writing signed card: {e}")
        sys.exit(1)


if __name__ == '__main__':
    main()


