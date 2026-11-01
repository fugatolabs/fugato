console.log(`Worker started for ${process.env.FUGATO_RUNTIME_ID || 'manual'}`);
const interval = setInterval(() => console.log('Worker heartbeat'), 5000);
const stop = () => clearInterval(interval);
process.once('SIGTERM', stop);
process.once('SIGINT', stop);
