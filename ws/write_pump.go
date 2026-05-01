package ws

// WritePump drains the client Send channel → pushes to WebSocket.
// Sends pings to detect dead connections.
