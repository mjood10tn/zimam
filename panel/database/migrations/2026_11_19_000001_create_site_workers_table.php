<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

/**
 * Each site's queue workers as last saved from the Workers tab (v0.4.0):
 * the panel's editing copy of the list site.workers applies. The agent's
 * state file is the truth on the server (R04-D16). A custom worker's
 * command is kept here, since it isn't a secret; the agent never stores it.
 */
return new class extends Migration
{
    public function up(): void
    {
        Schema::create('site_workers', function (Blueprint $table) {
            $table->id();
            $table->foreignId('site_id')->constrained()->cascadeOnDelete();
            $table->string('worker_id', 16);
            // laravel-queue or custom.
            $table->string('kind');
            $table->string('connection', 32)->nullable();
            $table->json('queues')->nullable();
            $table->unsignedTinyInteger('processes');
            // Null leaves artisan's own default.
            $table->unsignedInteger('tries')->nullable();
            $table->unsignedInteger('timeout')->nullable();
            $table->unsignedInteger('sleep')->nullable();
            $table->unsignedInteger('max_time')->nullable();
            $table->unsignedInteger('backoff')->nullable();
            $table->unsignedSmallInteger('memory_mb');
            $table->text('command')->nullable();
            // applying, active or failed.
            $table->string('status');
            $table->timestamps();
            $table->unique(['site_id', 'worker_id']);
        });
    }

    public function down(): void
    {
        Schema::dropIfExists('site_workers');
    }
};
