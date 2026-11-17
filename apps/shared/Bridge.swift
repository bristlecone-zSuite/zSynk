import Foundation

// The string-returning methods of the mobile framework answer with JSON
// envelopes instead of a thrown error (decision 0009): gomobile bridges a
// non-null string return and an out error pointer in a way that cannot
// express "nil on error" safely. These helpers unwrap the envelope for
// both apps that embed the engine; the shapes are the REST API's.

struct BridgeEnvelope<T: Decodable>: Decodable {
    let ok: Bool
    let data: T?
    let error: String?
}

struct BridgeError: LocalizedError {
    let message: String
    init(_ message: String) { self.message = message }
    var errorDescription: String? { message }
}

func decodeBridge<T: Decodable>(_ envelope: String) throws -> T {
    let wrapper = try JSONDecoder.synk.decode(BridgeEnvelope<T>.self, from: Data(envelope.utf8))
    if wrapper.ok, let data = wrapper.data {
        return data
    }
    throw BridgeError(wrapper.error ?? "the engine returned no answer")
}
