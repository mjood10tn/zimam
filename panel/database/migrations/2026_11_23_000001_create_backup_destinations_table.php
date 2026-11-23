<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

/**
 * Backup destinations (v0.5.0): S3-compatible storage or SFTP, each behind
 * an rclone crypt remote on the server (R05-D2). Only the non-secret fields
 * are kept: the S3 credentials and the encryption keys live in the server's
 * root-only rclone.conf and nowhere in the panel (R05-D3).
 */
return new class extends Migration
{
    public function up(): void
    {
        Schema::create('backup_destinations', function (Blueprint $table) {
            $table->id();
            // The agent's id: the crypt remote's name.
            $table->string('key', 24)->unique();
            // s3 or sftp.
            $table->string('type');
            // endpoint, region, bucket, prefix; or host, port, user, path.
            $table->json('settings');
            // SFTP: the key pair's public half the agent made, for the owner to install.
            $table->text('public_key')->nullable();
            // SFTP: the host key the first passed test pinned, or the one the owner gave.
            $table->string('host_key_fingerprint', 64)->nullable();
            // saving, active, failed or deleting.
            $table->string('status');
            $table->timestamp('last_tested_at')->nullable();
            $table->text('last_error')->nullable();
            $table->timestamps();
        });
    }

    public function down(): void
    {
        Schema::dropIfExists('backup_destinations');
    }
};
