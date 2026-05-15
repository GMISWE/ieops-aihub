"""Integration test for pyrage API surface used by backup.py.

Skipped when pyrage is not installed (local dev without backup deps);
CI installs the full [test] extra so this runs there.
"""
import pytest

pyrage = pytest.importorskip("pyrage")


def test_pyrage_roundtrip():
    # Generate a fresh X25519 keypair
    identity = pyrage.x25519.Identity.generate()
    recipient_str = str(identity.to_public())

    # API surface used by backup.py — if any of these break, backup breaks
    recipient = pyrage.x25519.Recipient.from_str(recipient_str)
    plaintext = b"some sqlite snapshot bytes"
    encrypted = pyrage.encrypt(plaintext, [recipient])

    assert encrypted != plaintext
    assert len(encrypted) > len(plaintext)  # age header + framing

    # Round-trip via the matching identity
    decrypted = pyrage.decrypt(encrypted, [identity])
    assert decrypted == plaintext
