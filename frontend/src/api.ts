export class ApiError extends Error {
    constructor(message: string, public status: number) { super(message); }
}

export async function api<T>(path: string, method = 'GET', body?: unknown, headers: Record<string, string> = {}): Promise<T> {
    const response = await fetch('/api' + path, {
        method,
        headers: { 'Content-Type': 'application/json', 'X-Ledger-Request': '1', ...headers },
        body: body === undefined ? undefined : JSON.stringify(body),
    });
    const text = await response.text();
    let data: any;
    try { data = text ? JSON.parse(text) : {}; }
    catch { throw new ApiError('The server returned an unexpected response. Please reload and try again.', response.status); }
    if (!response.ok) throw new ApiError(data.error || 'Request failed', response.status);
    return data;
}
