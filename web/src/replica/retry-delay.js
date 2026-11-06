export function syncRetryDelay(backoff,random=Math.random){return Math.min(30000,Math.round(backoff*(1+random()*.3)))}
