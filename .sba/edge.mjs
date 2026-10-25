// 标准套件复用同一个 ECH Worker，不添加第四个常驻云服务。
import ech from "../workers/ech-workers-cloudflare/src/index.js";
import { isAggregatorRoute, releaseMetadata, serveAggregatorArtifact } from "./aggregator-assets.mjs";

export default {
  async fetch(request, env, context) {
    const pathname = new URL(request.url).pathname;
    if (isAggregatorRoute(pathname)) return serveAggregatorArtifact(request, env);
    if (pathname === "/.well-known/easyproxy-release.json") {
      if (request.method !== "GET" && request.method !== "HEAD") {
        return new Response("Method not allowed", { status: 405, headers: { Allow: "GET, HEAD", "Cache-Control": "no-store" } });
      }
      const response = releaseMetadata(env);
      return request.method === "HEAD" ? new Response(null, response) : response;
    }
    if (env.SBA_PURPOSE === "preview") {
      return new Response("Preview isolated", { status: 403, headers: { "Cache-Control": "no-store" } });
    }
    return ech.fetch(request, env, context);
  }
};
