<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

/** Each deploy of a site and how it went; its log is its job's. */
return new class extends Migration
{
    public function up(): void
    {
        Schema::create('deployments', function (Blueprint $table) {
            $table->id();
            $table->foreignId('site_id')->constrained()->cascadeOnDelete();
            // The panel's agent_jobs row, which exists from the submit on,
            // even while it waits after busy. The deployment goes with it
            // when zimam:prune-history drops the job.
            $table->foreignId('agent_job_id')->constrained('agent_jobs')->cascadeOnDelete();
            // manual, webhook or url.
            $table->string('trigger');
            // The commit asked for, then the one deployed.
            $table->string('commit', 40)->nullable();
            // The release the deploy made live.
            $table->string('release')->nullable();
            // waiting, queued, running, succeeded, failed or cancelled, as its job.
            $table->string('status');
            $table->text('error_message')->nullable();
            // Null for a webhook or deploy URL.
            $table->foreignId('created_by')->nullable()->constrained('users')->nullOnDelete();
            $table->timestamp('started_at')->nullable();
            $table->timestamp('finished_at')->nullable();
            $table->timestamps();
            $table->index(['site_id', 'created_at']);
        });
    }

    public function down(): void
    {
        Schema::dropIfExists('deployments');
    }
};
