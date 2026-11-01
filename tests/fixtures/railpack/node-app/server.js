// Test fixture: answers every request with a fixed body. No dependencies, so
// the Railpack build needs only the Node toolchain.
const http = require("node:http");

const port = Number(process.env.PORT || 3000);
http
	.createServer((_req, res) => {
		res.setHeader("content-type", "text/plain");
		res.end("railpack-node-ok");
	})
	.listen(port, "0.0.0.0");
