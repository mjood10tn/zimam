<?php

namespace App\Agent;

use InvalidArgumentException;

/**
 * Reads /var/log/zimam/jobs/<id>.log, which the panel's user reads through
 * the zimam-agent group (§5.1). The path is built only from an id that
 * passed the ULID check, so it can't name any other file.
 */
final class FileJobLogReader implements JobLogReader
{
    public const JOB_ID = '/^[0-9A-HJKMNP-TV-Z]{26}\z/';

    /** At most this much is read per poll; the rest comes with the next. */
    public const MAX_BYTES = 256 * 1024;

    public function __construct(private readonly string $dir) {}

    public function read(string $jobId, int $offset): JobLogChunk
    {
        if (preg_match(self::JOB_ID, $jobId) !== 1) {
            throw new InvalidArgumentException('A job id must be a ULID');
        }
        $offset = max(0, $offset);
        $file = @fopen($this->dir.'/'.$jobId.'.log', 'rb');
        if ($file === false) {
            return new JobLogChunk([], $offset); // not written yet, or pruned
        }
        try {
            if (fseek($file, $offset) !== 0) {
                return new JobLogChunk([], $offset);
            }
            $data = (string) fread($file, self::MAX_BYTES);
        } finally {
            fclose($file);
        }
        $end = strrpos($data, "\n");
        if ($end === false) {
            return new JobLogChunk([], $offset);
        }

        return new JobLogChunk(explode("\n", substr($data, 0, $end)), $offset + $end + 1);
    }
}
