function cleanToken(value) {
  return String(value || "").trim();
}

function parseExpiry(value) {
  const raw = cleanToken(value);
  if (!raw) {
    return Number.NaN;
  }
  if (/^\d+$/.test(raw)) {
    const epoch = Number(raw);
    return raw.length <= 10 ? epoch * 1000 : epoch;
  }
  return Date.parse(raw);
}

export function selectAcceptedToken(requestedToken, env, now = Date.now()) {
  const requested = cleanToken(requestedToken)
    .split(",")
    .map((token) => token.trim())
    .filter(Boolean);
  const current = cleanToken(env.ECH_TOKEN);
  if (!current) {
    return { configured: false, token: "" };
  }
  if (requested.includes(current)) {
    return { configured: true, token: current };
  }

  const previous = cleanToken(env.ECH_TOKEN_PREVIOUS);
  const expiresAt = parseExpiry(env.ECH_TOKEN_PREVIOUS_EXPIRES_AT);
  if (previous && requested.includes(previous) && Number.isFinite(expiresAt) && now < expiresAt) {
    return { configured: true, token: previous };
  }
  return { configured: true, token: "" };
}
