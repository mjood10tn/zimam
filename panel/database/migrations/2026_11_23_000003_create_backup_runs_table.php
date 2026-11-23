<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

/**
 * Backup runs (v0.5.0), copied from the agent's run records by
 * zimam:sync-backup-runs every minute (R05-D9): timer runs have no panel
 * job to track. notified_at marks a finished run whose notification went
 * out (Task 10).
 */
return new class extends Migration
{
    public function up(): void
    {
        Schema::create('backup_runs', function (Blueprint $table) {
            $table->id();
            $table->foreignId('backup_plan_id')->constrained()->cascadeOnDelete();
            // yyyymmddThhmmssZ (R05-D6).
            $table->string('backup_id', 16);
            // timer or panel.
            $table->string('trigger');
            // running, succeeded or failed.
            $table->string('status');
            $table->unsignedBigInteger('size_bytes')->default(0);
            $table->json('parts');
            $table->text('error')->nullable();
            $table->timestamp('started_at');
            $table->timestamp('finished_at')->nullable();
            $table->timestamp('notified_at')->nullable();
            $table->timestamps();
            $table->unique(['backup_plan_id', 'backup_id']);
        });
    }

    public function down(): void
    {
        Schema::dropIfExists('backup_runs');
    }
};
