<?php

namespace App;

/**
 * What the app does with the site's MySQL database and Valkey user, as the
 * site's .env configures them (the DB_ and REDIS_ keys Zimam writes, and
 * the CACHE_STORE and QUEUE_CONNECTION the site chooses itself): one row, one
 * cache key, and a queue like Laravel's Redis queue, which runs its Lua
 * scripts on the queue's keys.
 */
final class Services
{
    /** Laravel's Redis cache lives in database 1, its queue in the default database 0. */
    private const CACHE_DATABASE = 1;

    private const QUEUE = 'queues:default';

    /** @var array<string, string> */
    private array $config;

    public function __construct(App $app)
    {
        $this->config = $app->config();
    }

    /** Writes a row to the site's database and reads it back. */
    public function row(string $word): string
    {
        $this->expect('DB_CONNECTION', 'mysql');
        $pdo = new \PDO(
            sprintf('mysql:host=%s;port=%s;dbname=%s', $this->need('DB_HOST'), $this->need('DB_PORT'), $this->need('DB_DATABASE')),
            $this->need('DB_USERNAME'),
            $this->need('DB_PASSWORD'),
            [\PDO::ATTR_ERRMODE => \PDO::ERRMODE_EXCEPTION],
        );
        $pdo->exec('CREATE TABLE IF NOT EXISTS e2e_rows (id INT AUTO_INCREMENT PRIMARY KEY, word VARCHAR(64) NOT NULL)');
        $insert = $pdo->prepare('INSERT INTO e2e_rows (word) VALUES (?)');
        $insert->execute([$word]);
        $select = $pdo->prepare('SELECT word FROM e2e_rows WHERE id = ?');
        $select->execute([$pdo->lastInsertId()]);

        return sprintf('database=%s user=%s row=%s', $this->need('DB_DATABASE'), $this->need('DB_USERNAME'), $select->fetchColumn());
    }

    /** Writes a key to the cache and reads it back. */
    public function cache(string $word): string
    {
        $this->expect('CACHE_STORE', 'redis');
        $redis = $this->redis(self::CACHE_DATABASE);
        $redis->set('e2e:cache:'.$word, $word);

        return sprintf('prefix=%s user=%s cache=%s', $this->need('REDIS_PREFIX'), $this->need('REDIS_USERNAME'), $redis->get('e2e:cache:'.$word));
    }

    /** Pushes a job, as Laravel's Redis queue does. */
    public function push(string $word): string
    {
        $this->expect('QUEUE_CONNECTION', 'redis');
        $payload = json_encode(['uuid' => bin2hex(random_bytes(8)), 'displayName' => 'e2e', 'attempts' => 0, 'data' => ['word' => $word]]);
        $this->redis(0)->eval(
            "redis.call('rpush', KEYS[1], ARGV[1]); redis.call('rpush', KEYS[2], 1)",
            [self::QUEUE, self::QUEUE.':notify', $payload],
            2,
        );

        return 'queued='.$word;
    }

    /** Whether a worker has run the job of that word. */
    public function done(string $word): string
    {
        $value = $this->redis(0)->get('e2e:done:'.$word);

        return $value === false ? 'done=no' : 'done='.$value;
    }

    /**
     * Runs the queue's jobs until the process is stopped, or one Redis call
     * fails, which it lets through: a closed connection ends the worker, as
     * it ends Laravel's. With $once it takes at most one job.
     *
     * @return int the exit status
     */
    public function work(bool $once): int
    {
        $this->expect('QUEUE_CONNECTION', 'redis');
        $redis = $this->redis(0);
        for (;;) {
            // Laravel's pop script: take a job, count the attempt, reserve it.
            $popped = $redis->eval(
                "local job = redis.call('lpop', KEYS[1])\n"
                ."local reserved = false\n"
                ."if(job ~= false) then\n"
                ."    reserved = cjson.decode(job)\n"
                ."    reserved['attempts'] = reserved['attempts'] + 1\n"
                ."    reserved = cjson.encode(reserved)\n"
                ."    redis.call('zadd', KEYS[2], ARGV[1], reserved)\n"
                ."    redis.call('lpop', KEYS[3])\n"
                ."end\n"
                .'return {job, reserved}',
                [self::QUEUE, self::QUEUE.':reserved', self::QUEUE.':notify', time() + 60],
                3,
            );
            if (is_array($popped) && is_string($popped[0] ?? null)) {
                $job = json_decode($popped[0], true);
                $word = (string) ($job['data']['word'] ?? '');
                $redis->set('e2e:done:'.$word, 'worker');
                $redis->zRem(self::QUEUE.':reserved', $popped[1]);
                echo "processed {$word}\n";
            } elseif (! $once) {
                usleep(200_000);
            }
            if ($once) {
                return 0;
            }
        }
    }

    private function redis(int $database): \Redis
    {
        $this->expect('REDIS_CLIENT', 'phpredis');
        $redis = new \Redis;
        $redis->connect($this->need('REDIS_HOST'), (int) $this->need('REDIS_PORT'));
        $redis->auth([$this->need('REDIS_USERNAME'), $this->need('REDIS_PASSWORD')]);
        $redis->setOption(\Redis::OPT_PREFIX, $this->need('REDIS_PREFIX'));
        $redis->select($database);

        return $redis;
    }

    private function need(string $key): string
    {
        $value = $this->config[$key] ?? '';
        if ($value === '') {
            throw new \RuntimeException("{$key} isn't set in the .env");
        }

        return $value;
    }

    private function expect(string $key, string $value): void
    {
        if (($this->config[$key] ?? '') !== $value) {
            throw new \RuntimeException("{$key} is ".var_export($this->config[$key] ?? null, true).", not {$value}");
        }
    }
}
