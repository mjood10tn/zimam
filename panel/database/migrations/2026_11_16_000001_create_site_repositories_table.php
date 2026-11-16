<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

/** The GitHub repository a site deploys from, and its deploy settings (R02-D4, R02-D11). */
return new class extends Migration
{
    public function up(): void
    {
        Schema::create('site_repositories', function (Blueprint $table) {
            $table->id();
            $table->foreignId('site_id')->unique()->constrained()->cascadeOnDelete();
            // github only (OQ1).
            $table->string('provider')->default('github');
            // owner/name, and the branch deploys take.
            $table->string('repository');
            $table->string('branch');
            // The deploy key's public half and SHA-256 fingerprint, once site.repo made it.
            $table->text('deploy_key_public')->nullable();
            $table->string('deploy_key_fingerprint')->nullable();
            // Laravel's encrypted cast: the webhook needs it back to verify signatures.
            $table->text('webhook_secret');
            // The deploy URL's token, only as its SHA-256.
            $table->string('deploy_token_hash', 64);
            $table->boolean('auto_deploy')->default(false);
            // Run by the site user's bash, at most 64 KiB (R02-D2).
            $table->text('deploy_script');
            $table->boolean('run_composer')->default(true);
            // 1 to 20 (OQ5).
            $table->unsignedTinyInteger('keep_releases')->default(5);
            $table->timestamps();
        });
    }

    public function down(): void
    {
        Schema::dropIfExists('site_repositories');
    }
};
