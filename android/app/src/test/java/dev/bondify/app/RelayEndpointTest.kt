package dev.bondify.app

import org.junit.Assert.assertEquals
import org.junit.Assert.assertThrows
import org.junit.Test

class RelayEndpointTest {
    @Test
    fun parsesHostnameAndPort() {
        assertEquals(
            RelayEndpoint("relay.example.com", 51820),
            parseRelayEndpoint("relay.example.com:51820"),
        )
    }

    @Test
    fun parsesIpv4AndPort() {
        assertEquals(
            RelayEndpoint("192.0.2.10", 443),
            parseRelayEndpoint("192.0.2.10:443"),
        )
    }

    @Test
    fun parsesBracketedIpv6AndPort() {
        assertEquals(
            RelayEndpoint("2001:db8::10", 51820),
            parseRelayEndpoint("[2001:db8::10]:51820"),
        )
    }

    @Test
    fun rejectsMissingOrInvalidPort() {
        for (input in listOf("relay.example.com", "relay.example.com:0", "relay.example.com:70000")) {
            assertThrows(IllegalArgumentException::class.java) {
                parseRelayEndpoint(input)
            }
        }
    }

    @Test
    fun rejectsUrlComponentsAndCredentials() {
        for (input in listOf("user@relay.example.com:51820", "relay.example.com:51820/path")) {
            assertThrows(IllegalArgumentException::class.java) {
                parseRelayEndpoint(input)
            }
        }
    }
}
